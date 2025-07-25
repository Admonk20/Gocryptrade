package websocket

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thrasher-corp/gocryptotrader/common"
	"github.com/thrasher-corp/gocryptotrader/config"
	"github.com/thrasher-corp/gocryptotrader/currency"
	"github.com/thrasher-corp/gocryptotrader/encoding/json"
	"github.com/thrasher-corp/gocryptotrader/exchanges/protocol"
	"github.com/thrasher-corp/gocryptotrader/exchanges/request"
	"github.com/thrasher-corp/gocryptotrader/exchanges/subscription"
	mockws "github.com/thrasher-corp/gocryptotrader/internal/testing/websocket"
)

const (
	Ping          = "ping"
	useProxyTests = false                     // Disabled by default. Freely available proxy servers that work all the time are difficult to find
	proxyURL      = "http://212.186.171.4:80" // Replace with a usable proxy server
)

var errDastardlyReason = errors.New("some dastardly reason")

type testStruct struct {
	Error error
	WC    connection
}

type testRequest struct {
	Event        string          `json:"event"`
	RequestID    int64           `json:"reqid,omitempty"`
	Pairs        []string        `json:"pair"`
	Subscription testRequestData `json:"subscription"`
}

// testRequestData contains details on WS channel
type testRequestData struct {
	Name     string `json:"name,omitempty"`
	Interval int64  `json:"interval,omitempty"`
	Depth    int64  `json:"depth,omitempty"`
}

type testResponse struct {
	RequestID int64 `json:"reqid,omitempty"`
}

type testSubKey struct {
	Mood string
}

func newDefaultSetup() *ManagerSetup {
	return &ManagerSetup{
		ExchangeConfig: &config.Exchange{
			Features: &config.FeaturesConfig{
				Enabled: config.FeaturesEnabledConfig{Websocket: true},
			},
			API: config.APIConfig{
				AuthenticatedWebsocketSupport: true,
			},
			WebsocketTrafficTimeout: time.Second * 5,
			Name:                    "GTX",
		},
		DefaultURL:   "testDefaultURL",
		RunningURL:   "wss://testRunningURL",
		Connector:    func() error { return nil },
		Subscriber:   func(subscription.List) error { return nil },
		Unsubscriber: func(subscription.List) error { return nil },
		GenerateSubscriptions: func() (subscription.List, error) {
			return subscription.List{
				{Channel: "TestSub"},
				{Channel: "TestSub2", Key: "purple"},
				{Channel: "TestSub3", Key: testSubKey{"mauve"}},
				{Channel: "TestSub4", Key: 42},
			}, nil
		},
		Features: &protocol.Features{Subscribe: true, Unsubscribe: true},
	}
}

func TestSetup(t *testing.T) {
	t.Parallel()
	var w *Manager
	err := w.Setup(nil)
	assert.ErrorContains(t, err, "nil pointer: *websocket.Manager")

	w = &Manager{DataHandler: make(chan any)}
	err = w.Setup(nil)
	assert.ErrorContains(t, err, "nil pointer: *websocket.ManagerSetup")

	websocketSetup := &ManagerSetup{}
	err = w.Setup(websocketSetup)
	assert.ErrorContains(t, err, "nil pointer: ManagerSetup.Exchange")

	websocketSetup.ExchangeConfig = &config.Exchange{}
	err = w.Setup(websocketSetup)
	assert.ErrorContains(t, err, "nil pointer: ManagerSetup.ExchangeConfig.Features")

	websocketSetup.ExchangeConfig.Features = &config.FeaturesConfig{}
	err = w.Setup(websocketSetup)
	assert.ErrorContains(t, err, "nil pointer: ManagerSetup.Features")

	websocketSetup.Features = &protocol.Features{}
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errExchangeConfigNameEmpty)

	websocketSetup.ExchangeConfig.Name = "testname"
	websocketSetup.Subscriber = func(subscription.List) error { return nil } // kicks off the setup
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errWebsocketConnectorUnset)
	websocketSetup.Subscriber = nil

	websocketSetup.Connector = func() error { return nil }
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errWebsocketSubscriberUnset)

	websocketSetup.Subscriber = func(subscription.List) error { return nil }
	w.features.Unsubscribe = true
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errWebsocketUnsubscriberUnset)

	websocketSetup.Unsubscriber = func(subscription.List) error { return nil }
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errWebsocketSubscriptionsGeneratorUnset)

	websocketSetup.GenerateSubscriptions = func() (subscription.List, error) { return nil, nil }
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errDefaultURLIsEmpty)

	websocketSetup.DefaultURL = "test"
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errRunningURLIsEmpty)

	websocketSetup.RunningURL = "http://www.google.com"
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errInvalidWebsocketURL)

	websocketSetup.RunningURL = "wss://www.google.com"
	websocketSetup.RunningURLAuth = "http://www.google.com"
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errInvalidWebsocketURL)

	websocketSetup.RunningURLAuth = "wss://www.google.com"
	err = w.Setup(websocketSetup)
	assert.ErrorIs(t, err, errInvalidTrafficTimeout)

	websocketSetup.ExchangeConfig.WebsocketTrafficTimeout = time.Minute
	err = w.Setup(websocketSetup)
	assert.NoError(t, err, "Setup should not error")
}

func TestConnectionMessageErrors(t *testing.T) {
	t.Parallel()
	wsWrong := &Manager{}
	wsWrong.connector = func() error { return nil }
	err := wsWrong.Connect()
	assert.ErrorIs(t, err, ErrWebsocketNotEnabled, "Connect should error correctly")

	wsWrong.setEnabled(true)
	wsWrong.setState(connectingState)
	err = wsWrong.Connect()
	assert.ErrorIs(t, err, errAlreadyReconnecting, "Connect should error correctly")

	wsWrong.setState(disconnectedState)
	err = wsWrong.Connect()
	assert.ErrorIs(t, err, common.ErrNilPointer, "Connect should get a nil pointer error")
	assert.ErrorContains(t, err, "subscriptions", "Connect should get a nil pointer error about subscriptions")

	wsWrong.subscriptions = subscription.NewStore()
	wsWrong.setState(disconnectedState)
	wsWrong.connector = func() error { return errDastardlyReason }
	err = wsWrong.Connect()
	assert.ErrorIs(t, err, errDastardlyReason, "Connect should error correctly")

	ws := NewManager()
	err = ws.Setup(newDefaultSetup())
	require.NoError(t, err, "Setup must not error")
	ws.trafficTimeout = time.Minute
	ws.connector = connect

	require.ErrorIs(t, ws.Connect(), ErrSubscriptionsNotAdded)
	require.NoError(t, ws.Shutdown())

	ws.Subscriber = func(subs subscription.List) error {
		for _, sub := range subs {
			if err := ws.subscriptions.Add(sub); err != nil {
				return err
			}
		}
		return nil
	}
	require.NoError(t, ws.Connect(), "Connect must not error")

	checkToRoutineResult := func(t *testing.T) {
		t.Helper()
		v, ok := <-ws.ToRoutine
		require.True(t, ok, "ToRoutine must not be closed on us")
		switch err := v.(type) {
		case *gws.CloseError:
			assert.Equal(t, "SpecialText", err.Text, "Should get correct Close Error")
		case error:
			assert.ErrorIs(t, err, errDastardlyReason, "Should get the correct error")
		default:
			assert.Failf(t, "Wrong data type sent to ToRoutine", "Got type: %T", err)
		}
	}

	ws.TrafficAlert <- struct{}{}
	ws.ReadMessageErrors <- errDastardlyReason
	checkToRoutineResult(t)

	ws.ReadMessageErrors <- &gws.CloseError{Code: 1006, Text: "SpecialText"}
	checkToRoutineResult(t)

	// Test individual connection defined functions
	require.NoError(t, ws.Shutdown())
	ws.useMultiConnectionManagement = true

	err = ws.Connect()
	assert.ErrorIs(t, err, errNoPendingConnections, "Connect should error correctly")

	ws.useMultiConnectionManagement = true
	ws.SetCanUseAuthenticatedEndpoints(true)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()
	ws.connectionManager = []*connectionWrapper{{setup: &ConnectionSetup{URL: "ws" + mock.URL[len("http"):] + "/ws"}}}
	err = ws.Connect()
	require.ErrorIs(t, err, errWebsocketSubscriptionsGeneratorUnset)

	ws.connectionManager[0].setup.Authenticate = func(context.Context, Connection) error { return errDastardlyReason }

	ws.connectionManager[0].setup.GenerateSubscriptions = func() (subscription.List, error) {
		return nil, errDastardlyReason
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errDastardlyReason)

	ws.connectionManager[0].setup.GenerateSubscriptions = func() (subscription.List, error) {
		return subscription.List{{Channel: "test"}}, nil
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errNoConnectFunc)

	ws.connectionManager[0].setup.Connector = func(context.Context, Connection) error {
		return errDastardlyReason
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errWebsocketDataHandlerUnset)

	ws.connectionManager[0].setup.Handler = func(context.Context, []byte) error {
		return errDastardlyReason
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errWebsocketSubscriberUnset)

	ws.connectionManager[0].setup.Subscriber = func(context.Context, Connection, subscription.List) error {
		return errDastardlyReason
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errDastardlyReason)

	ws.connectionManager[0].setup.Connector = func(ctx context.Context, conn Connection) error {
		return conn.Dial(ctx, gws.DefaultDialer, nil)
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errDastardlyReason)

	ws.connectionManager[0].setup.Handler = func(context.Context, []byte) error {
		return errDastardlyReason
	}
	err = ws.Connect()
	require.ErrorIs(t, err, errDastardlyReason)

	ws.connectionManager[0].setup.Subscriber = func(context.Context, Connection, subscription.List) error {
		return errDastardlyReason
	}
	ws.connectionManager[0].setup.Authenticate = nil
	err = ws.Connect()
	require.ErrorIs(t, err, errDastardlyReason)
	require.NoError(t, ws.shutdown())

	ws.connectionManager[0].setup.Subscriber = func(context.Context, Connection, subscription.List) error {
		return nil
	}
	err = ws.Connect()
	require.ErrorIs(t, err, ErrSubscriptionsNotAdded)
	require.NoError(t, ws.shutdown())

	ws.connectionManager[0].subscriptions = subscription.NewStore()
	ws.connectionManager[0].setup.Subscriber = func(context.Context, Connection, subscription.List) error {
		return ws.connectionManager[0].subscriptions.Add(&subscription.Subscription{Channel: "test"})
	}
	err = ws.Connect()
	require.NoError(t, err)

	err = ws.connectionManager[0].connection.SendRawMessage(t.Context(), request.Unset, gws.TextMessage, []byte("test"))
	require.NoError(t, err)

	require.NoError(t, err)
	require.NoError(t, ws.Shutdown())
}

func TestManager(t *testing.T) {
	t.Parallel()

	ws := NewManager()

	err := ws.SetProxyAddress("garbagio")
	assert.ErrorContains(t, err, "invalid URI for request", "SetProxyAddress should error correctly")

	ws.setEnabled(true)
	defaultSetup := newDefaultSetup()
	err = ws.Setup(defaultSetup) // Sets to enabled again
	require.NoError(t, err, "Setup may not error")

	err = ws.Setup(defaultSetup)
	assert.ErrorIs(t, err, errWebsocketAlreadyInitialised, "Setup should error correctly if called twice")

	assert.Equal(t, "GTX", ws.GetName(), "GetName should return correctly")
	assert.True(t, ws.IsEnabled(), "Websocket should be enabled by Setup")

	ws.setEnabled(false)
	assert.False(t, ws.IsEnabled(), "Websocket should be disabled by setEnabled(false)")

	ws.setEnabled(true)
	assert.True(t, ws.IsEnabled(), "Websocket should be enabled by setEnabled(true)")

	err = ws.SetProxyAddress("https://192.168.0.1:1337")
	assert.NoError(t, err, "SetProxyAddress should not error when not yet connected")

	ws.setState(connectedState)

	ws.connector = func() error { return errDastardlyReason }
	err = ws.SetProxyAddress("https://192.168.0.1:1336")
	assert.ErrorIs(t, err, errDastardlyReason, "SetProxyAddress should call Connect and error from there")

	err = ws.SetProxyAddress("https://192.168.0.1:1336")
	assert.ErrorIs(t, err, errSameProxyAddress, "SetProxyAddress should error correctly")

	// removing proxy
	assert.NoError(t, ws.SetProxyAddress(""))

	ws.setEnabled(true)
	// reinstate proxy
	err = ws.SetProxyAddress("http://localhost:1337")
	assert.NoError(t, err, "SetProxyAddress should not error")
	assert.Equal(t, "http://localhost:1337", ws.GetProxyAddress(), "GetProxyAddress should return correctly")
	assert.Equal(t, "wss://testRunningURL", ws.GetWebsocketURL(), "GetWebsocketURL should return correctly")
	assert.Equal(t, time.Second*5, ws.trafficTimeout, "trafficTimeout should default correctly")

	assert.ErrorIs(t, ws.Shutdown(), ErrNotConnected)
	ws.setState(connectedState)
	assert.NoError(t, ws.Shutdown())

	ws.connector = func() error { return nil }

	require.ErrorIs(t, ws.Connect(), ErrSubscriptionsNotAdded)
	require.NoError(t, ws.Shutdown())

	ws.Subscriber = func(subs subscription.List) error {
		for _, sub := range subs {
			if err := ws.subscriptions.Add(sub); err != nil {
				return err
			}
		}
		return nil
	}
	assert.NoError(t, ws.Connect(), "Connect should not error")

	ws.defaultURL = "ws://demos.kaazing.com/echo"
	ws.defaultURLAuth = "ws://demos.kaazing.com/echo"

	err = ws.SetWebsocketURL("", false, false)
	assert.NoError(t, err, "SetWebsocketURL should not error")

	err = ws.SetWebsocketURL("ws://demos.kaazing.com/echo", false, false)
	assert.NoError(t, err, "SetWebsocketURL should not error")

	err = ws.SetWebsocketURL("", true, false)
	assert.NoError(t, err, "SetWebsocketURL should not error")

	err = ws.SetWebsocketURL("ws://demos.kaazing.com/echo", true, false)
	assert.NoError(t, err, "SetWebsocketURL should not error")

	err = ws.SetWebsocketURL("ws://demos.kaazing.com/echo", true, true)
	assert.NoError(t, err, "SetWebsocketURL should not error on reconnect")

	// -- initiate the reconnect which is usually handled by connection monitor
	err = ws.Connect()
	assert.NoError(t, err, "ReConnect called manually should not error")

	err = ws.Connect()
	assert.ErrorIs(t, err, errAlreadyConnected, "ReConnect should error when already connected")

	err = ws.Shutdown()
	assert.NoError(t, err, "Shutdown should not error")
	ws.Wg.Wait()

	ws.useMultiConnectionManagement = true

	ws.connectionManager = []*connectionWrapper{{setup: &ConnectionSetup{URL: "ws://demos.kaazing.com/echo"}, connection: &connection{}}}
	err = ws.SetProxyAddress("https://192.168.0.1:1337")
	require.NoError(t, err)
}

// TestSetCanUseAuthenticatedEndpoints logic test
func TestSetCanUseAuthenticatedEndpoints(t *testing.T) {
	t.Parallel()
	ws := NewManager()
	assert.False(t, ws.CanUseAuthenticatedEndpoints(), "CanUseAuthenticatedEndpoints should return false")
	ws.SetCanUseAuthenticatedEndpoints(true)
	assert.True(t, ws.CanUseAuthenticatedEndpoints(), "CanUseAuthenticatedEndpoints should return true")
}

// TestDial logic test
func TestDial(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	testCases := []testStruct{
		{
			WC: connection{
				ExchangeName:     "test1",
				Verbose:          true,
				URL:              "ws" + mock.URL[len("http"):] + "/ws",
				RateLimit:        request.NewWeightedRateLimitByDuration(10 * time.Millisecond),
				ResponseMaxLimit: 7000000000,
			},
		},
		{
			Error: errors.New(" Error: malformed ws or wss URL"),
			WC: connection{
				ExchangeName:     "test2",
				Verbose:          true,
				URL:              "",
				ResponseMaxLimit: 7000000000,
			},
		},
		{
			WC: connection{
				ExchangeName:     "test3",
				Verbose:          true,
				URL:              "ws" + mock.URL[len("http"):] + "/ws",
				ProxyURL:         proxyURL,
				ResponseMaxLimit: 7000000000,
			},
		},
	}
	// Mock server rejects parallel connections
	for i := range testCases {
		if testCases[i].WC.ProxyURL != "" && !useProxyTests {
			t.Log("Proxy testing not enabled, skipping")
			continue
		}
		err := testCases[i].WC.Dial(t.Context(), &gws.Dialer{}, http.Header{})
		if err != nil {
			if testCases[i].Error != nil && strings.Contains(err.Error(), testCases[i].Error.Error()) {
				return
			}
			t.Fatal(err)
		}
	}
}

// TestSendMessage logic test
func TestSendMessage(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	testCases := []testStruct{
		{
			WC: connection{
				ExchangeName:     "test1",
				Verbose:          true,
				URL:              "ws" + mock.URL[len("http"):] + "/ws",
				RateLimit:        request.NewWeightedRateLimitByDuration(10 * time.Millisecond),
				ResponseMaxLimit: 7000000000,
			},
		},
		{
			Error: errors.New(" Error: malformed ws or wss URL"),
			WC: connection{
				ExchangeName:     "test2",
				Verbose:          true,
				URL:              "",
				ResponseMaxLimit: 7000000000,
			},
		},
		{
			WC: connection{
				ExchangeName:     "test3",
				Verbose:          true,
				URL:              "ws" + mock.URL[len("http"):] + "/ws",
				ProxyURL:         proxyURL,
				ResponseMaxLimit: 7000000000,
			},
		},
	}
	// Mock server rejects parallel connections
	for x := range testCases {
		if testCases[x].WC.ProxyURL != "" && !useProxyTests {
			t.Log("Proxy testing not enabled, skipping")
			continue
		}
		err := testCases[x].WC.Dial(t.Context(), &gws.Dialer{}, http.Header{})
		if err != nil {
			if testCases[x].Error != nil && strings.Contains(err.Error(), testCases[x].Error.Error()) {
				return
			}
			t.Fatal(err)
		}
		err = testCases[x].WC.SendJSONMessage(t.Context(), request.Unset, Ping)
		require.NoError(t, err)
		err = testCases[x].WC.SendRawMessage(t.Context(), request.Unset, gws.TextMessage, []byte(Ping))
		require.NoError(t, err)
	}
}

func TestSendMessageReturnResponse(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	wc := &connection{
		Verbose:          true,
		URL:              "ws" + mock.URL[len("http"):] + "/ws",
		ResponseMaxLimit: time.Second * 5,
		Match:            NewMatch(),
	}
	if wc.ProxyURL != "" && !useProxyTests {
		t.Skip("Proxy testing not enabled, skipping")
	}

	err := wc.Dial(t.Context(), &gws.Dialer{}, http.Header{})
	if err != nil {
		t.Fatal(err)
	}

	go readMessages(t, wc)

	req := testRequest{
		Event: "subscribe",
		Pairs: []string{currency.NewPairWithDelimiter("XBT", "USD", "/").String()},
		Subscription: testRequestData{
			Name: "ticker",
		},
		RequestID: wc.GenerateMessageID(false),
	}

	_, err = wc.SendMessageReturnResponse(t.Context(), request.Unset, req.RequestID, req)
	if err != nil {
		t.Error(err)
	}

	cancelledCtx, fn := context.WithDeadline(t.Context(), time.Now())
	fn()
	_, err = wc.SendMessageReturnResponse(cancelledCtx, request.Unset, "123", req)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// with timeout
	wc.ResponseMaxLimit = 1
	_, err = wc.SendMessageReturnResponse(t.Context(), request.Unset, "123", req)
	assert.ErrorIs(t, err, ErrSignatureTimeout, "SendMessageReturnResponse should error when request ID not found")

	_, err = wc.SendMessageReturnResponsesWithInspector(t.Context(), request.Unset, "123", req, 1, inspection{})
	assert.ErrorIs(t, err, ErrSignatureTimeout, "SendMessageReturnResponse should error when request ID not found")
}

func TestWaitForResponses(t *testing.T) {
	t.Parallel()
	dummy := &connection{
		ResponseMaxLimit: time.Nanosecond,
		Match:            NewMatch(),
	}
	_, err := dummy.waitForResponses(t.Context(), "silly", nil, 1, inspection{})
	require.ErrorIs(t, err, ErrSignatureTimeout)

	dummy.ResponseMaxLimit = time.Second
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = dummy.waitForResponses(ctx, "silly", nil, 1, inspection{})
	require.ErrorIs(t, err, context.Canceled)

	// test break early and hit verbose path
	ch := make(chan []byte, 1)
	ch <- []byte("hello")
	ctx = request.WithVerbose(t.Context())

	got, err := dummy.waitForResponses(ctx, "silly", ch, 2, inspection{breakEarly: true})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "hello", string(got[0]))
}

type inspection struct {
	breakEarly bool
}

func (i inspection) IsFinal([]byte) bool { return i.breakEarly }

type reporter struct {
	name string
	msg  []byte
	t    time.Duration
}

func (r *reporter) Latency(name string, message []byte, t time.Duration) {
	r.name = name
	r.msg = message
	r.t = t
}

// readMessages helper func
func readMessages(t *testing.T, wc *connection) {
	t.Helper()
	timer := time.NewTimer(20 * time.Second)
	for {
		select {
		case <-timer.C:
			return
		default:
			resp := wc.ReadMessage()
			if resp.Raw == nil {
				t.Error("connection has closed")
				return
			}
			var incoming testResponse
			err := json.Unmarshal(resp.Raw, &incoming)
			if err != nil {
				t.Error(err)
				return
			}
			if incoming.RequestID > 0 {
				wc.Match.IncomingWithData(incoming.RequestID, resp.Raw)
				return
			}
		}
	}
}

// TestSetupPingHandler logic test
func TestSetupPingHandler(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	wc := &connection{
		URL:              "ws" + mock.URL[len("http"):] + "/ws",
		ResponseMaxLimit: time.Second * 5,
		Match:            NewMatch(),
		Wg:               &sync.WaitGroup{},
	}

	if wc.ProxyURL != "" && !useProxyTests {
		t.Skip("Proxy testing not enabled, skipping")
	}
	wc.shutdown = make(chan struct{})
	err := wc.Dial(t.Context(), &gws.Dialer{}, http.Header{})
	if err != nil {
		t.Fatal(err)
	}

	wc.SetupPingHandler(request.Unset, PingHandler{
		UseGorillaHandler: true,
		MessageType:       gws.PingMessage,
		Delay:             100,
	})

	err = wc.Connection.Close()
	if err != nil {
		t.Error(err)
	}

	err = wc.Dial(t.Context(), &gws.Dialer{}, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	wc.SetupPingHandler(request.Unset, PingHandler{
		MessageType: gws.TextMessage,
		Message:     []byte(Ping),
		Delay:       200,
	})
	time.Sleep(time.Millisecond * 201)
	close(wc.shutdown)
	wc.Wg.Wait()
}

// TestParseBinaryResponse logic test
func TestParseBinaryResponse(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	wc := &connection{
		URL:              "ws" + mock.URL[len("http"):] + "/ws",
		ResponseMaxLimit: time.Second * 5,
		Match:            NewMatch(),
	}

	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	_, err := g.Write([]byte("hello"))
	require.NoError(t, err, "gzip.Write must not error")
	assert.NoError(t, g.Close(), "Close should not error")

	resp, err := wc.parseBinaryResponse(b.Bytes())
	assert.NoError(t, err, "parseBinaryResponse should not error parsing gzip")
	assert.EqualValues(t, "hello", resp, "parseBinaryResponse should decode gzip")

	b.Reset()
	f, err := flate.NewWriter(&b, 1)
	require.NoError(t, err, "flate.NewWriter must not error")
	_, err = f.Write([]byte("goodbye"))
	require.NoError(t, err, "flate.Write must not error")
	assert.NoError(t, f.Close(), "Close should not error")

	resp, err = wc.parseBinaryResponse(b.Bytes())
	assert.NoError(t, err, "parseBinaryResponse should not error parsing inflate")
	assert.EqualValues(t, "goodbye", resp, "parseBinaryResponse should deflate")

	_, err = wc.parseBinaryResponse([]byte{})
	assert.ErrorContains(t, err, "unexpected EOF", "parseBinaryResponse should error on empty input")
}

// TestCanUseAuthenticatedWebsocketForWrapper logic test
func TestCanUseAuthenticatedWebsocketForWrapper(t *testing.T) {
	t.Parallel()
	ws := &Manager{}
	assert.False(t, ws.CanUseAuthenticatedWebsocketForWrapper(), "CanUseAuthenticatedWebsocketForWrapper should return false")

	ws.setState(connectedState)
	require.True(t, ws.IsConnected(), "IsConnected must return true")
	assert.False(t, ws.CanUseAuthenticatedWebsocketForWrapper(), "CanUseAuthenticatedWebsocketForWrapper should return false")

	ws.SetCanUseAuthenticatedEndpoints(true)
	assert.True(t, ws.CanUseAuthenticatedWebsocketForWrapper(), "CanUseAuthenticatedWebsocketForWrapper should return true")
}

func TestGenerateMessageID(t *testing.T) {
	t.Parallel()
	wc := connection{}
	const spins = 1000
	ids := make([]int64, spins)
	for i := range spins {
		id := wc.GenerateMessageID(true)
		assert.NotContains(t, ids, id, "GenerateMessageID should not generate the same ID twice")
		ids[i] = id
	}

	wc.bespokeGenerateMessageID = func(bool) int64 { return 42 }
	assert.EqualValues(t, 42, wc.GenerateMessageID(true), "GenerateMessageID should use bespokeGenerateMessageID")
}

// 7002502	       166.7 ns/op	      48 B/op	       3 allocs/op
func BenchmarkGenerateMessageID_High(b *testing.B) {
	wc := connection{}
	for b.Loop() {
		_ = wc.GenerateMessageID(true)
	}
}

// 6536250	       186.1 ns/op	      48 B/op	       3 allocs/op
func BenchmarkGenerateMessageID_Low(b *testing.B) {
	wc := connection{}
	for b.Loop() {
		_ = wc.GenerateMessageID(false)
	}
}

func TestCheckWebsocketURL(t *testing.T) {
	err := checkWebsocketURL("")
	assert.ErrorIs(t, err, errInvalidWebsocketURL, "checkWebsocketURL should error correctly on empty string")

	err = checkWebsocketURL("wowowow:wowowowo")
	assert.ErrorIs(t, err, errInvalidWebsocketURL, "checkWebsocketURL should error correctly on bad format")

	err = checkWebsocketURL("://")
	assert.ErrorContains(t, err, "missing protocol scheme", "checkWebsocketURL should error correctly on bad proto")

	err = checkWebsocketURL("http://www.google.com")
	assert.ErrorIs(t, err, errInvalidWebsocketURL, "checkWebsocketURL should error correctly on wrong proto")

	err = checkWebsocketURL("wss://websocketconnection.place")
	assert.NoError(t, err, "checkWebsocketURL should not error")

	err = checkWebsocketURL("ws://websocketconnection.place")
	assert.NoError(t, err, "checkWebsocketURL should not error")
}

// GenSubs defines a theoretical exchange with pair management
type GenSubs struct {
	EnabledPairs currency.Pairs
	subscribos   subscription.List
	unsubscribos subscription.List
}

// generateSubs default subs created from the enabled pairs list
func (g *GenSubs) generateSubs() (subscription.List, error) {
	superduperchannelsubs := make(subscription.List, len(g.EnabledPairs))
	for i := range g.EnabledPairs {
		superduperchannelsubs[i] = &subscription.Subscription{
			Channel: "TEST:" + strconv.FormatInt(int64(i), 10),
			Pairs:   currency.Pairs{g.EnabledPairs[i]},
		}
	}
	return superduperchannelsubs, nil
}

func (g *GenSubs) SUBME(subs subscription.List) error {
	if len(subs) == 0 {
		return errors.New("WOW")
	}
	g.subscribos = subs
	return nil
}

func (g *GenSubs) UNSUBME(unsubs subscription.List) error {
	if len(unsubs) == 0 {
		return errors.New("WOW")
	}
	g.unsubscribos = unsubs
	return nil
}

// sneaky connect func
func connect() error { return nil }

func TestFlushChannels(t *testing.T) {
	t.Parallel()
	// Enabled pairs/setup system

	dodgyWs := Manager{}
	err := dodgyWs.FlushChannels()
	assert.ErrorIs(t, err, ErrWebsocketNotEnabled, "FlushChannels should error correctly")

	dodgyWs.setEnabled(true)
	err = dodgyWs.FlushChannels()
	assert.ErrorIs(t, err, ErrNotConnected, "FlushChannels should error correctly")

	newgen := GenSubs{EnabledPairs: []currency.Pair{
		currency.NewPair(currency.BTC, currency.AUD),
		currency.NewBTCUSDT(),
	}}

	w := NewManager()
	w.exchangeName = "test"
	w.connector = connect
	w.Subscriber = newgen.SUBME
	w.Unsubscriber = newgen.UNSUBME
	// Added for when we utilise connect() in FlushChannels() so the traffic monitor doesn't time out and turn this to an unconnected state
	w.trafficTimeout = time.Second * 30

	w.setEnabled(true)
	w.setState(connectedState)

	// Allow subscribe and unsubscribe feature set, without these the tests will call shutdown and connect.
	w.features.Subscribe = true
	w.features.Unsubscribe = true

	// Disable pair and flush system
	newgen.EnabledPairs = []currency.Pair{currency.NewPair(currency.BTC, currency.AUD)}
	w.GenerateSubs = func() (subscription.List, error) { return subscription.List{{Channel: "test"}}, nil }

	require.ErrorIs(t, w.FlushChannels(), ErrSubscriptionsNotAdded, "FlushChannels must error correctly on no subscriptions added")

	w.Subscriber = func(subs subscription.List) error {
		for _, sub := range subs {
			if err := w.subscriptions.Add(sub); err != nil {
				return err
			}
		}
		return nil
	}

	require.NoError(t, w.FlushChannels(), "FlushChannels must not error")

	w.GenerateSubs = func() (subscription.List, error) { return nil, errDastardlyReason } // error on generateSubs
	err = w.FlushChannels()                                                               // error on full subscribeToChannels
	assert.ErrorIs(t, err, errDastardlyReason, "FlushChannels should error correctly on GenerateSubs")

	w.GenerateSubs = func() (subscription.List, error) { return nil, nil } // No subs to sub

	require.ErrorIs(t, w.FlushChannels(), ErrSubscriptionsNotRemoved)

	w.Unsubscriber = func(subs subscription.List) error {
		for _, sub := range subs {
			if err := w.subscriptions.Remove(sub); err != nil {
				return err
			}
		}
		return nil
	}
	assert.NoError(t, w.FlushChannels(), "FlushChannels should not error")

	w.GenerateSubs = newgen.generateSubs
	subs, err := w.GenerateSubs()
	require.NoError(t, err, "GenerateSubs must not error")
	require.NoError(t, w.AddSubscriptions(nil, subs...), "AddSubscriptions must not error")
	err = w.FlushChannels()
	assert.NoError(t, err, "FlushChannels should not error")

	w.GenerateSubs = newgen.generateSubs
	w.subscriptions = subscription.NewStore()
	err = w.subscriptions.Add(&subscription.Subscription{
		Key:     41,
		Channel: "match channel",
		Pairs:   currency.Pairs{currency.NewPair(currency.BTC, currency.AUD)},
	})
	require.NoError(t, err, "AddSubscription must not error")
	err = w.subscriptions.Add(&subscription.Subscription{
		Key:     42,
		Channel: "unsub channel",
		Pairs:   currency.Pairs{currency.NewPair(currency.THETA, currency.USDT)},
	})
	require.NoError(t, err, "AddSubscription must not error")

	err = w.FlushChannels()
	assert.NoError(t, err, "FlushChannels should not error")

	w.setState(connectedState)
	err = w.FlushChannels()
	assert.NoError(t, err, "FlushChannels should not error")

	// Multi connection management
	w.useMultiConnectionManagement = true
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	w.subscriptions = subscription.NewStore()

	amazingCandidate := &ConnectionSetup{
		URL: "ws" + mock.URL[len("http"):] + "/ws",
		Connector: func(ctx context.Context, conn Connection) error {
			return conn.Dial(ctx, gws.DefaultDialer, nil)
		},
		GenerateSubscriptions: newgen.generateSubs,
		Subscriber:            func(context.Context, Connection, subscription.List) error { return nil },
		Unsubscriber:          func(context.Context, Connection, subscription.List) error { return nil },
		Handler:               func(context.Context, []byte) error { return nil },
	}
	require.NoError(t, w.SetupNewConnection(amazingCandidate))
	require.ErrorIs(t, w.FlushChannels(), ErrSubscriptionsNotAdded, "Must error when no subscriptions are added to the subscription store")

	w.connectionManager[0].setup.Subscriber = func(ctx context.Context, c Connection, s subscription.List) error {
		return currySimpleSubConn(w)(ctx, c, s)
	}
	require.NoError(t, w.FlushChannels(), "FlushChannels must not error")

	// Forces full connection cycle (shutdown, connect, subscribe). This will also start monitoring routines.
	w.features.Subscribe = false
	require.NoError(t, w.FlushChannels(), "FlushChannels must not error")

	// Unsubscribe what's already subscribed. No subscriptions left over, which then forces the shutdown and removal
	// of the connection from management.
	w.features.Subscribe = true
	w.connectionManager[0].setup.GenerateSubscriptions = func() (subscription.List, error) { return nil, nil }
	require.ErrorIs(t, w.FlushChannels(), ErrSubscriptionsNotRemoved, "Must error when no subscriptions are removed from subscription store")

	w.connectionManager[0].setup.Unsubscriber = func(ctx context.Context, c Connection, s subscription.List) error {
		return currySimpleUnsubConn(w)(ctx, c, s)
	}
	require.NoError(t, w.FlushChannels(), "FlushChannels must not error")
}

func TestDisable(t *testing.T) {
	t.Parallel()
	w := NewManager()
	w.setEnabled(true)
	w.setState(connectedState)
	require.NoError(t, w.Disable(), "Disable must not error")
	assert.ErrorIs(t, w.Disable(), ErrAlreadyDisabled, "Disable should error correctly")
}

func TestEnable(t *testing.T) {
	t.Parallel()
	w := NewManager()
	w.connector = connect
	w.Subscriber = func(subscription.List) error { return nil }
	w.Unsubscriber = func(subscription.List) error { return nil }
	w.GenerateSubs = func() (subscription.List, error) { return nil, nil }
	require.NoError(t, w.Enable(), "Enable must not error")
	assert.ErrorIs(t, w.Enable(), errWebsocketAlreadyEnabled, "Enable should error correctly")
}

func TestSetupNewConnection(t *testing.T) {
	t.Parallel()
	var nonsenseWebsock *Manager
	err := nonsenseWebsock.SetupNewConnection(&ConnectionSetup{URL: "urlstring"})
	assert.ErrorContains(t, err, "nil pointer: *websocket.Manager")

	nonsenseWebsock = &Manager{}
	err = nonsenseWebsock.SetupNewConnection(&ConnectionSetup{URL: "urlstring"})
	assert.ErrorIs(t, err, errExchangeConfigNameEmpty, "SetupNewConnection should error correctly")

	nonsenseWebsock = &Manager{exchangeName: "test"}
	err = nonsenseWebsock.SetupNewConnection(&ConnectionSetup{URL: "urlstring"})
	assert.ErrorIs(t, err, errTrafficAlertNil, "SetupNewConnection should error correctly")

	nonsenseWebsock.TrafficAlert = make(chan struct{}, 1)
	err = nonsenseWebsock.SetupNewConnection(&ConnectionSetup{URL: "urlstring"})
	assert.ErrorIs(t, err, errReadMessageErrorsNil, "SetupNewConnection should error correctly")

	web := NewManager()

	err = web.Setup(newDefaultSetup())
	assert.NoError(t, err, "Setup should not error")

	err = web.SetupNewConnection(&ConnectionSetup{URL: "urlstring"})
	assert.NoError(t, err, "SetupNewConnection should not error")

	err = web.SetupNewConnection(&ConnectionSetup{URL: "urlstring", Authenticated: true})
	assert.NoError(t, err, "SetupNewConnection should not error")

	// Test connection candidates for multi connection tracking.
	multi := NewManager()
	set := newDefaultSetup()
	set.UseMultiConnectionManagement = true
	require.NoError(t, multi.Setup(set))

	err = multi.SetupNewConnection(nil)
	assert.ErrorContains(t, err, "nil pointer: *websocket.ConnectionSetup")

	connSetup := &ConnectionSetup{ResponseCheckTimeout: time.Millisecond}
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errDefaultURLIsEmpty)

	connSetup.URL = "urlstring"
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errWebsocketConnectorUnset)

	connSetup.Connector = func(context.Context, Connection) error { return nil }
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errWebsocketSubscriptionsGeneratorUnset)

	connSetup.GenerateSubscriptions = func() (subscription.List, error) { return nil, nil }
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errWebsocketSubscriberUnset)

	connSetup.Subscriber = func(context.Context, Connection, subscription.List) error { return nil }
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errWebsocketUnsubscriberUnset)

	connSetup.Unsubscriber = func(context.Context, Connection, subscription.List) error { return nil }
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errWebsocketDataHandlerUnset)

	connSetup.Handler = func(context.Context, []byte) error { return nil }
	connSetup.MessageFilter = []string{"slices are super naughty and not comparable"}
	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errMessageFilterNotComparable)

	connSetup.MessageFilter = "comparable string signature"
	err = multi.SetupNewConnection(connSetup)
	require.NoError(t, err)

	require.Len(t, multi.connectionManager, 1)

	require.Nil(t, multi.AuthConn)
	require.Nil(t, multi.Conn)

	err = multi.SetupNewConnection(connSetup)
	require.ErrorIs(t, err, errDuplicateConnectionSetup)
}

func TestConnectionShutdown(t *testing.T) {
	t.Parallel()
	wc := connection{shutdown: make(chan struct{})}
	err := wc.Shutdown()
	assert.NoError(t, err, "Shutdown should not error")

	err = wc.Dial(t.Context(), &gws.Dialer{}, nil)
	assert.ErrorContains(t, err, "malformed ws or wss URL", "Dial should error correctly")

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	wc.URL = "ws" + mock.URL[len("http"):] + "/ws"

	err = wc.Dial(t.Context(), &gws.Dialer{}, nil)
	require.NoError(t, err, "Dial must not error")

	err = wc.Shutdown()
	require.NoError(t, err, "Shutdown must not error")
}

// TestLatency logic test
func TestLatency(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockws.WsMockUpgrader(t, w, r, mockws.EchoHandler) }))
	defer mock.Close()

	r := &reporter{}
	exch := "Kraken"
	wc := &connection{
		ExchangeName:     exch,
		Verbose:          true,
		URL:              "ws" + mock.URL[len("http"):] + "/ws",
		ResponseMaxLimit: time.Second * 1,
		Match:            NewMatch(),
		Reporter:         r,
	}
	if wc.ProxyURL != "" && !useProxyTests {
		t.Skip("Proxy testing not enabled, skipping")
	}

	err := wc.Dial(t.Context(), &gws.Dialer{}, http.Header{})
	require.NoError(t, err)

	go readMessages(t, wc)

	req := testRequest{
		Event:        "subscribe",
		Pairs:        []string{currency.NewPairWithDelimiter("XBT", "USD", "/").String()},
		Subscription: testRequestData{Name: "ticker"},
		RequestID:    wc.GenerateMessageID(false),
	}

	_, err = wc.SendMessageReturnResponse(t.Context(), request.Unset, req.RequestID, req)
	require.NoError(t, err)
	require.NotEmpty(t, r.t, "Latency must have a duration")
	require.Equal(t, exch, r.name, "Latency must have the correct exchange name")
}

func TestRemoveURLQueryString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://www.google.com", removeURLQueryString("https://www.google.com?test=1"), "removeURLQueryString should remove query string")
	assert.Equal(t, "https://www.google.com", removeURLQueryString("https://www.google.com"), "removeURLQueryString should not change URL")
	assert.Empty(t, removeURLQueryString(""), "removeURLQueryString should be empty")
}

func TestWriteToConn(t *testing.T) {
	t.Parallel()
	wc := connection{}
	require.ErrorIs(t, wc.writeToConn(t.Context(), request.Unset, func() error { return nil }), errWebsocketIsDisconnected)
	wc.setConnectedStatus(true)
	// No rate limits set
	require.NoError(t, wc.writeToConn(t.Context(), request.Unset, func() error { return nil }))
	// connection rate limit set
	wc.RateLimit = request.NewWeightedRateLimitByDuration(time.Millisecond)
	require.NoError(t, wc.writeToConn(t.Context(), request.Unset, func() error { return nil }))
	ctx, cancel := context.WithTimeout(t.Context(), 0) // deadline exceeded
	cancel()
	require.ErrorIs(t, wc.writeToConn(ctx, request.Unset, func() error { return nil }), context.DeadlineExceeded)
	// definitions set but with fallover
	wc.RateLimitDefinitions = request.RateLimitDefinitions{
		request.Auth: request.NewWeightedRateLimitByDuration(time.Millisecond),
	}
	require.NoError(t, wc.writeToConn(t.Context(), request.Unset, func() error { return nil }))
	// match with global rate limit
	require.NoError(t, wc.writeToConn(t.Context(), request.Auth, func() error { return nil }))
	// definitions set but connection rate limiter not set
	wc.RateLimit = nil
	require.ErrorIs(t, wc.writeToConn(ctx, request.Unset, func() error { return nil }), errRateLimitNotFound)
}

func TestDrain(t *testing.T) {
	t.Parallel()
	drain(nil)
	ch := make(chan error)
	drain(ch)
	require.Empty(t, ch, "Drain must empty the channel")
	ch = make(chan error, 10)
	for range 10 {
		ch <- errors.New("test")
	}
	drain(ch)
	require.Empty(t, ch, "Drain must empty the channel")
}

func TestMonitorFrame(t *testing.T) {
	t.Parallel()
	ws := Manager{}
	require.Panics(t, func() { ws.monitorFrame(nil, nil) }, "monitorFrame must panic on nil frame")
	require.Panics(t, func() { ws.monitorFrame(nil, func() func() bool { return nil }) }, "monitorFrame must panic on nil function")
	ws.Wg.Add(1)
	ws.monitorFrame(&ws.Wg, func() func() bool { return func() bool { return true } })
	ws.Wg.Wait()
}

func TestMonitorData(t *testing.T) {
	t.Parallel()
	ws := Manager{ShutdownC: make(chan struct{}), DataHandler: make(chan any, 10)}
	// Handle shutdown signal
	close(ws.ShutdownC)
	require.True(t, ws.observeData(nil))
	ws.ShutdownC = make(chan struct{})
	// Handle blockage of ToRoutine
	go func() { ws.DataHandler <- nil }()
	var dropped int
	require.False(t, ws.observeData(&dropped))
	require.Equal(t, 1, dropped)
	// Handle reinstate of ToRoutine functionality which will reset dropped counter
	ws.ToRoutine = make(chan any, 10)
	go func() { ws.DataHandler <- nil }()
	require.False(t, ws.observeData(&dropped))
	require.Empty(t, dropped)
	// Handle outer closure shell
	innerShell := ws.monitorData()
	go func() { ws.DataHandler <- nil }()
	require.False(t, innerShell())
	// Handle shutdown signal
	close(ws.ShutdownC)
	require.True(t, innerShell())
}

func TestMonitorConnection(t *testing.T) {
	t.Parallel()
	ws := Manager{verbose: true, ReadMessageErrors: make(chan error, 1), ShutdownC: make(chan struct{})}
	// Handle timer expired and websocket disabled, shutdown everything.
	timer := time.NewTimer(0)
	ws.setState(connectedState)
	ws.connectionMonitorRunning.Store(true)
	require.True(t, ws.observeConnection(timer))
	require.False(t, ws.connectionMonitorRunning.Load())
	require.Equal(t, disconnectedState, ws.state.Load())
	// Handle timer expired and everything is great, reset the timer.
	ws.setEnabled(true)
	ws.setState(connectedState)
	ws.connectionMonitorRunning.Store(true)
	timer = time.NewTimer(0)
	require.False(t, ws.observeConnection(timer)) // Not shutting down
	// Handle timer expired and for reason its not connected, so lets happily connect again.
	ws.setState(disconnectedState)
	require.False(t, ws.observeConnection(timer)) // Connect is intentionally erroring
	// Handle error from a connection which will then trigger a reconnect
	ws.setState(connectedState)
	ws.DataHandler = make(chan any, 1)
	ws.ReadMessageErrors <- errConnectionFault
	timer = time.NewTimer(time.Second)
	require.False(t, ws.observeConnection(timer))
	payload := <-ws.DataHandler
	err, ok := payload.(error)
	require.True(t, ok)
	require.ErrorIs(t, err, errConnectionFault)
	// Handle outta closure shell
	innerShell := ws.monitorConnection()
	ws.setState(connectedState)
	ws.ReadMessageErrors <- errConnectionFault
	require.False(t, innerShell())
}

func TestMonitorTraffic(t *testing.T) {
	t.Parallel()
	ws := Manager{verbose: true, ShutdownC: make(chan struct{}), TrafficAlert: make(chan struct{}, 1)}
	ws.Wg.Add(1)
	// Handle external shutdown signal
	timer := time.NewTimer(time.Second)
	close(ws.ShutdownC)
	require.True(t, ws.observeTraffic(timer))
	// Handle timer expired but system is connecting, so reset the timer
	ws.ShutdownC = make(chan struct{})
	ws.setState(connectingState)
	timer = time.NewTimer(0)
	require.False(t, ws.observeTraffic(timer))
	// Handle timer expired and system is connected and has traffic within time window
	ws.setState(connectedState)
	timer = time.NewTimer(0)
	ws.TrafficAlert <- struct{}{}
	require.False(t, ws.observeTraffic(timer))
	// Handle timer expired and system is connected but no traffic within time window, causes shutdown to occur.
	timer = time.NewTimer(0)
	require.True(t, ws.observeTraffic(timer))
	ws.Wg.Done()
	// Shutdown is done in a routine, so we need to wait for it to finish
	require.Eventually(t, func() bool { return disconnectedState == ws.state.Load() }, time.Second, time.Millisecond)
	// Handle outer closure shell
	innerShell := ws.monitorTraffic()
	ws.m.Lock()
	ws.ShutdownC = make(chan struct{})
	ws.m.Unlock()
	ws.setState(connectedState)
	ws.TrafficAlert <- struct{}{}
	require.False(t, innerShell())
}

func TestGetConnection(t *testing.T) {
	t.Parallel()
	var ws *Manager
	_, err := ws.GetConnection(nil)
	require.ErrorIs(t, err, common.ErrNilPointer)
	require.ErrorContains(t, err, fmt.Sprintf("%T", ws))

	ws = &Manager{}

	_, err = ws.GetConnection(nil)
	require.ErrorIs(t, err, common.ErrNilPointer)
	require.ErrorContains(t, err, "messageFilter")

	_, err = ws.GetConnection("testURL")
	require.ErrorIs(t, err, errCannotObtainOutboundConnection)

	ws.useMultiConnectionManagement = true

	_, err = ws.GetConnection("testURL")
	require.ErrorIs(t, err, ErrNotConnected)

	ws.setState(connectedState)

	_, err = ws.GetConnection("testURL")
	require.ErrorIs(t, err, ErrRequestRouteNotFound)

	ws.connectionManager = []*connectionWrapper{{
		setup: &ConnectionSetup{MessageFilter: "testURL", URL: "testURL"},
	}}

	_, err = ws.GetConnection("testURL")
	require.ErrorIs(t, err, ErrNotConnected)

	expected := &connection{}
	ws.connectionManager[0].connection = expected

	conn, err := ws.GetConnection("testURL")
	require.NoError(t, err)
	assert.Same(t, expected, conn)
}
