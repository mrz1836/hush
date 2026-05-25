package discord

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// syncBuffer is a goroutine-safe wrapper around bytes.Buffer used by
// tests that read the captured slog output while audit-mirror
// goroutines may still be writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// sessionShim is a programmable in-memory replacement for
// *discordgo.Session used by the package's tests. It exposes
// trigger helpers for synchronously firing the registered handlers,
// and recording slots for outbound calls.
//
// All fields are protected by mu except the handler slots, which are
// goroutine-safe via copy-then-invoke.
type sessionShim struct {
	mu sync.Mutex

	// Recorded outbound calls.
	openCalls            int
	closeCalls           int
	dms                  []dmRecord
	created              []string
	allMessages          []sentMessage
	interactionResponses []interactionResponseRecord

	// Programmable behaviour.
	openErr   error
	createErr error
	sendErr   map[string]error // keyed by channelID; missing key == nil
	sendCallN map[string]int   // count of calls per channelID
	sendOnce  map[string]error // returns this once then clears (for delivery-failure-then-success tests)
	// sendBlock, when non-nil, makes ChannelMessageSendComplex block on a
	// receive from this channel before returning. Tests use it to simulate
	// a hung Discord REST call (e.g. discordgo sleeping through a
	// rate-limit Retry-After) so the ctx-bounded send wrapper in
	// BotApprover.RequestApproval can be exercised.
	sendBlock chan struct{}

	// Registered handlers.
	interactionHandlers []func(*discordgo.Session, *discordgo.InteractionCreate)
	connectHandlers     []func(*discordgo.Session, *discordgo.Connect)
	disconnectHandlers  []func(*discordgo.Session, *discordgo.Disconnect)
	readyHandlers       []func(*discordgo.Session, *discordgo.Ready)
	resumedHandlers     []func(*discordgo.Session, *discordgo.Resumed)
}

type dmRecord struct {
	ChannelID string
	Send      *discordgo.MessageSend
}

type sentMessage struct {
	ChannelID string
	Send      *discordgo.MessageSend
}

type interactionResponseRecord struct {
	Interaction *discordgo.Interaction
	Response    *discordgo.InteractionResponse
}

func newSessionShim() *sessionShim {
	return &sessionShim{
		sendErr:   make(map[string]error),
		sendCallN: make(map[string]int),
		sendOnce:  make(map[string]error),
	}
}

func (s *sessionShim) Open() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openCalls++
	return s.openErr
}

func (s *sessionShim) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return nil
}

func (s *sessionShim) UserChannelCreate(recipientID string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.created = append(s.created, recipientID)
	return &discordgo.Channel{ID: "dm:" + recipientID}, nil
}

func (s *sessionShim) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	s.mu.Lock()
	block := s.sendBlock
	if once, ok := s.sendOnce[channelID]; ok {
		delete(s.sendOnce, channelID)
		s.sendCallN[channelID]++
		s.mu.Unlock()
		return nil, once
	}
	if err, ok := s.sendErr[channelID]; ok && err != nil {
		s.sendCallN[channelID]++
		s.mu.Unlock()
		return nil, err
	}
	s.sendCallN[channelID]++
	rec := sentMessage{ChannelID: channelID, Send: data}
	s.allMessages = append(s.allMessages, rec)
	if strings.HasPrefix(channelID, "dm:") {
		s.dms = append(s.dms, dmRecord{ChannelID: channelID, Send: data})
	}
	s.mu.Unlock()
	// Hang point — release only when the test signals via the channel
	// (or it's closed). Models the discordgo "sleep through Retry-After"
	// hang that necessitates ctx-bounded send in BotApprover.
	if block != nil {
		<-block
	}
	return &discordgo.Message{ID: "msg:" + channelID}, nil
}

func (s *sessionShim) InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interactionResponses = append(s.interactionResponses, interactionResponseRecord{
		Interaction: interaction,
		Response:    resp,
	})
	return nil
}

func (s *sessionShim) AddHandler(handler interface{}) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch h := handler.(type) {
	case func(*discordgo.Session, *discordgo.InteractionCreate):
		s.interactionHandlers = append(s.interactionHandlers, h)
	case func(*discordgo.Session, *discordgo.Connect):
		s.connectHandlers = append(s.connectHandlers, h)
	case func(*discordgo.Session, *discordgo.Disconnect):
		s.disconnectHandlers = append(s.disconnectHandlers, h)
	case func(*discordgo.Session, *discordgo.Ready):
		s.readyHandlers = append(s.readyHandlers, h)
	case func(*discordgo.Session, *discordgo.Resumed):
		s.resumedHandlers = append(s.resumedHandlers, h)
	}
	return func() {}
}

// TriggerReady invokes every registered Ready handler synchronously.
func (s *sessionShim) TriggerReady() {
	s.mu.Lock()
	hs := append([]func(*discordgo.Session, *discordgo.Ready){}, s.readyHandlers...)
	s.mu.Unlock()
	for _, h := range hs {
		h(nil, &discordgo.Ready{})
	}
}

// TriggerResumed invokes every registered Resumed handler.
func (s *sessionShim) TriggerResumed() {
	s.mu.Lock()
	hs := append([]func(*discordgo.Session, *discordgo.Resumed){}, s.resumedHandlers...)
	s.mu.Unlock()
	for _, h := range hs {
		h(nil, &discordgo.Resumed{})
	}
}

// TriggerDisconnect invokes every registered Disconnect handler.
func (s *sessionShim) TriggerDisconnect() {
	s.mu.Lock()
	hs := append([]func(*discordgo.Session, *discordgo.Disconnect){}, s.disconnectHandlers...)
	s.mu.Unlock()
	for _, h := range hs {
		h(nil, &discordgo.Disconnect{})
	}
}

// testOwnerID is the canonical Discord user ID used by tests as the
// configured operator. Every test in this package sets
// BotConfig.OwnerID = testOwnerID so that the shim's default
// TriggerInteractionCreate populates a Member.User that satisfies the
// onInteractionCreate owner check.
const testOwnerID = "owner"

// TriggerInteractionCreate invokes every registered InteractionCreate
// handler with a synthetic message-component interaction whose
// CustomID is set to customID (e.g. "uuid:approve"). Sets the
// Interaction.Member.User.ID to testOwnerID so the production owner
// check accepts it; use TriggerInteractionCreateAs to simulate a
// non-owner click.
func (s *sessionShim) TriggerInteractionCreate(customID string) {
	s.TriggerInteractionCreateAs(customID, testOwnerID)
}

// TriggerInteractionCreateAs is the explicit-user-ID form of
// TriggerInteractionCreate. Populates Interaction.Member.User.ID with
// the supplied userID. When userID == "" the interaction has neither
// Member nor User — used to exercise the malformed-payload defensive
// path.
func (s *sessionShim) TriggerInteractionCreateAs(customID, userID string) {
	ic := &discordgo.InteractionCreate{
		Interaction: buildInteractionWithUser(customID, userID).Interaction,
	}
	s.mu.Lock()
	hs := append([]func(*discordgo.Session, *discordgo.InteractionCreate){}, s.interactionHandlers...)
	s.mu.Unlock()
	for _, h := range hs {
		h(nil, ic)
	}
}

// buildInteractionWithUser is the package-private constructor shared
// by the shim and the unit-test helper buildButtonInteraction.
func buildInteractionWithUser(customID, userID string) *discordgo.InteractionCreate {
	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent,
			Data: discordgo.MessageComponentInteractionData{
				CustomID:      customID,
				ComponentType: discordgo.ButtonComponent,
			},
		},
	}
	if userID != "" {
		ic.Member = &discordgo.Member{User: &discordgo.User{ID: userID}}
	}
	return ic
}

// SetSendErr programs the shim to return err for every call to
// ChannelMessageSendComplex with the given channelID.
func (s *sessionShim) SetSendErr(channelID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendErr[channelID] = err
}

// SetSendOnceErr programs the shim to return err on the next call
// for channelID, then clear the override (subsequent calls succeed).
func (s *sessionShim) SetSendOnceErr(channelID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendOnce[channelID] = err
}

// LastDM returns the most recent DM record (channel + payload).
// Returns false when no DMs have been sent.
func (s *sessionShim) LastDM() (dmRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dms) == 0 {
		return dmRecord{}, false
	}
	return s.dms[len(s.dms)-1], true
}

// DMCount returns the number of owner DMs the shim has recorded.
func (s *sessionShim) DMCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dms)
}

// DMAt returns the DM at index i (0-based; oldest first).
func (s *sessionShim) DMAt(i int) (dmRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.dms) {
		return dmRecord{}, false
	}
	return s.dms[i], true
}

// SentMessagesFor returns a copy of every message sent to channelID.
func (s *sessionShim) SentMessagesFor(channelID string) []*discordgo.MessageSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*discordgo.MessageSend
	for _, m := range s.allMessages {
		if m.ChannelID == channelID {
			out = append(out, m.Send)
		}
	}
	return out
}

// AllSentMessages returns a copy of every recorded message regardless
// of channel.
func (s *sessionShim) AllSentMessages() []sentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sentMessage, len(s.allMessages))
	copy(out, s.allMessages)
	return out
}

func (s *sessionShim) InteractionResponses() []interactionResponseRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]interactionResponseRecord, len(s.interactionResponses))
	copy(out, s.interactionResponses)
	return out
}

// CloseCalls returns the count of Close() invocations.
func (s *sessionShim) CloseCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

// OpenCalls returns the count of Open() invocations.
func (s *sessionShim) OpenCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openCalls
}

// SetOpenErr programs Open() to return err for every subsequent call.
func (s *sessionShim) SetOpenErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openErr = err
}

// SetCreateErr programs UserChannelCreate to return err.
func (s *sessionShim) SetCreateErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createErr = err
}

// SetSendBlock atomically programs the channel that makes the next
// ChannelMessageSendComplex call block. Passing nil disarms.
func (s *sessionShim) SetSendBlock(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendBlock = ch
}

// newTestApprover wires a BotApprover against the shim with a
// deterministic logger and a tight reconnect cadence so test
// timeouts stay short.
func newTestApprover(ctx context.Context, shim *sessionShim, cfg BotConfig, logger *slog.Logger) *BotApprover {
	a := newBotApproverWithSession(ctx, cfg, logger, shim)
	a.reconnectBaseDelay = time.Millisecond
	a.reconnectMaxDelay = 5 * time.Millisecond
	return a
}

var (
	errShimSendFail   = errors.New("shim: send failed")
	errShimCreateFail = errors.New("shim: create failed")
	errShimOpenFail   = errors.New("shim: open failed")
)
