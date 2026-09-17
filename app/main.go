// Command edge-portal is a minimal operator-facing web portal meant to run
// on an embedded Linux device (e.g. a Raspberry Pi) next to an MQTT broker.
//
// It does two jobs:
//  1. Subscribes to an MQTT topic and keeps the most recently received
//     JSON payload in memory.
//  2. Serves a single, auto-refreshing HTML page showing that payload,
//     the device name and the current MQTT connection status.
//
// There is intentionally no database, no JavaScript and no external
// dependency beyond an MQTT client library — the goal is something that
// stays alive and legible on constrained hardware.
package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

//go:embed templates/index.html
var templatesFS embed.FS

var pageTemplate = template.Must(template.ParseFS(templatesFS, "templates/index.html"))

// KeyValue is one field extracted from the last JSON payload, rendered as
// a row/card on the portal page. Using a flat slice (instead of a raw map)
// lets the template iterate over fields in a stable, sorted order.
type KeyValue struct {
	Key   string
	Value string
}

// State is the in-memory "database" of this portal: whatever we currently
// know about the MQTT connection and the last message we saw. All access
// goes through the mutex because the HTTP handler (reader) and the MQTT
// callback (writer) run on different goroutines.
type State struct {
	mu           sync.RWMutex
	connected    bool
	values       []KeyValue
	rawPayload   string
	lastUpdate   time.Time
	messagesSeen int
	lastError    string
}

func (s *State) setConnected(connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = connected
}

func (s *State) recordMessage(raw []byte, values []KeyValue, parseErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messagesSeen++
	s.lastUpdate = time.Now()
	s.rawPayload = string(raw)
	if parseErr != nil {
		s.lastError = parseErr.Error()
		return
	}
	s.values = values
	s.lastError = ""
}

func (s *State) snapshot() (connected bool, values []KeyValue, raw string, lastUpdate time.Time, count int, lastErr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected, s.values, s.rawPayload, s.lastUpdate, s.messagesSeen, s.lastError
}

var state = &State{}

// PageData is the view model handed to the HTML template. Keeping it
// separate from State means the template never has to know about mutexes.
type PageData struct {
	DeviceName   string
	Broker       string
	Topic        string
	StatusLabel  string
	StatusClass  string
	Values       []KeyValue
	RawPayload   string
	LastUpdate   string
	LastError    string
	MessagesSeen int
	HasData      bool
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	deviceName := getenv("DEVICE_NAME", "Edge Device 01")
	broker := getenv("MQTT_BROKER", "tcp://mosquitto:1883")
	topic := getenv("MQTT_TOPIC", "devices/edge01/telemetry")
	clientID := getenv("MQTT_CLIENT_ID", "edge-portal")
	httpPort := getenv("HTTP_PORT", "8080")

	client := newMQTTClient(broker, clientID, topic)
	go connectWithRetry(client, broker)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleIndex(w, r, deviceName, broker, topic)
	})
	mux.HandleFunc("/healthz", handleHealth)

	log.Printf("edge-portal: listening on :%s (device=%q broker=%q topic=%q)", httpPort, deviceName, broker, topic)
	if err := http.ListenAndServe(":"+httpPort, mux); err != nil {
		log.Fatalf("http server stopped: %v", err)
	}
}

// newMQTTClient wires up connection lifecycle callbacks. The actual
// Subscribe call happens in OnConnect so that automatic reconnects
// re-subscribe as well.
func newMQTTClient(broker, clientID, topic string) mqtt.Client {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetryInterval(3 * time.Second)
	opts.SetKeepAlive(30 * time.Second)

	opts.OnConnect = func(c mqtt.Client) {
		log.Printf("mqtt: connected to %s", broker)
		state.setConnected(true)
		if token := c.Subscribe(topic, 1, onMessage); token.Wait() && token.Error() != nil {
			log.Printf("mqtt: subscribe to %q failed: %v", topic, token.Error())
		} else {
			log.Printf("mqtt: subscribed to %q", topic)
		}
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		log.Printf("mqtt: connection lost: %v", err)
		state.setConnected(false)
	}

	return mqtt.NewClient(opts)
}

// connectWithRetry keeps trying to reach the broker forever. This matters
// on an edge device where the broker container (or the network) may come
// up after the portal does.
func connectWithRetry(client mqtt.Client, broker string) {
	for {
		token := client.Connect()
		token.Wait()
		if err := token.Error(); err != nil {
			log.Printf("mqtt: connect to %s failed: %v (retrying in 3s)", broker, err)
			state.setConnected(false)
			time.Sleep(3 * time.Second)
			continue
		}
		return // AutoReconnect takes over from here
	}
}

// onMessage is the MQTT callback. It tries to parse the payload as JSON
// and flattens it into sorted key/value pairs for display. Non-JSON
// payloads are still recorded (raw + an error note) rather than dropped,
// so the operator can see that *something* arrived even if it's malformed.
func onMessage(_ mqtt.Client, msg mqtt.Message) {
	raw := msg.Payload()

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		log.Printf("mqtt: payload on %s was not valid JSON: %v", msg.Topic(), err)
		state.recordMessage(raw, nil, err)
		return
	}

	values := make([]KeyValue, 0, len(parsed))
	for k, v := range parsed {
		values = append(values, KeyValue{Key: k, Value: formatValue(v)})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })

	state.recordMessage(raw, values, nil)
}

func formatValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func handleIndex(w http.ResponseWriter, _ *http.Request, deviceName, broker, topic string) {
	connected, values, raw, lastUpdate, count, lastErr := state.snapshot()

	data := PageData{
		DeviceName:   deviceName,
		Broker:       broker,
		Topic:        topic,
		Values:       values,
		RawPayload:   raw,
		LastError:    lastErr,
		MessagesSeen: count,
		HasData:      count > 0,
	}

	if connected {
		data.StatusLabel = "CONNECTED"
		data.StatusClass = "status-ok"
	} else {
		data.StatusLabel = "DISCONNECTED"
		data.StatusClass = "status-bad"
	}

	if lastUpdate.IsZero() {
		data.LastUpdate = "No messages received yet"
	} else {
		data.LastUpdate = lastUpdate.Format("2006-01-02 15:04:05 MST")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
