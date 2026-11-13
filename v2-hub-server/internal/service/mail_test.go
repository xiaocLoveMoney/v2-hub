package service

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/mail"
)

// fakeSender 记下发出的邮件；fail 非空时每次发送都返回它。
type fakeSender struct {
	mu   sync.Mutex
	sent []mail.Message
	fail error
}

func (s *fakeSender) Send(_ context.Context, m mail.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *fakeSender) messages() []mail.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail.Message(nil), s.sent...)
}

// accountFixture：在 portalFixture（内存面板、节点组、用户服务、终端用户认证）之上接系统设置、假发信器与账号自助服务。
type accountFixture struct {
	*portalFixture
	settings *SettingsService
	sender   *fakeSender
	mail     *MailService
	account  *AccountService
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	pf := newPortalFixture(t)
	settings := NewSettingsService(pf.pool, SubscriptionConfig{InfoNodes: true}, pf.clock.now)
	sender := &fakeSender{}
	mails := NewMailService(pf.pool, testSealer(t), sender, settings, pf.clock.now)
	account := NewAccountService(pf.pool, testSealer(t), AccountDeps{Settings: settings, Mail: mails, Users: pf.users, Portal: pf.portal}, pf.clock.now)
	return &accountFixture{portalFixture: pf, settings: settings, sender: sender, mail: mails, account: account}
}

func (f *accountFixture) update(t *testing.T, patch map[string]any) (Settings, error) {
	t.Helper()
	return updateSettings(t, f.settings, f.actor, patch)
}

func (f *accountFixture) flush(t *testing.T) {
	t.Helper()
	if _, err := f.mail.ProcessOutbox(context.Background(), time.Minute); err != nil {
		t.Fatalf("ProcessOutbox: %v", err)
	}
}

func (f *accountFixture) enqueue(t *testing.T, to string, priority int16) {
	t.Helper()
	err := f.mail.inTx(context.Background(), func(q *sqlcgen.Queries) error {
		return f.mail.enqueue(context.Background(), q, to, mailContent{Subject: "s-" + to, Text: "text-" + to, HTML: "<p>html</p>"}, priority)
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

type outboxRow struct {
	status   string
	attempts int
	hasBody  bool
	lastErr  string
}

func (f *accountFixture) outbox(t *testing.T, to string) outboxRow {
	t.Helper()
	var r outboxRow
	err := f.pool.QueryRow(context.Background(), "SELECT status, attempts, body_enc IS NOT NULL, last_error FROM mail_outbox WHERE recipient = $1 ORDER BY id DESC LIMIT 1", to).
		Scan(&r.status, &r.attempts, &r.hasBody, &r.lastErr)
	if err != nil {
		t.Fatalf("outbox row for %s: %v", to, err)
	}
	return r
}

func TestMailOutboxSendsAndClearsBody(t *testing.T) {
	f := newAccountFixture(t)
	f.enqueue(t, "alice@example.com", mailPriorityNotice)

	var stored []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT body_enc FROM mail_outbox").Scan(&stored); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(stored), "text-alice") {
		t.Fatal("mail body must be encrypted at rest")
	}
	f.flush(t)
	msgs := f.sender.messages()
	if len(msgs) != 1 || msgs[0].To != "alice@example.com" || msgs[0].Text != "text-alice@example.com" || msgs[0].HTML == "" {
		t.Fatalf("sent = %+v", msgs)
	}
	if r := f.outbox(t, "alice@example.com"); r.status != MailStatusSent || r.hasBody || r.attempts != 1 {
		t.Fatalf("after send = %+v, want sent with body cleared", r)
	}
	f.flush(t)
	if len(f.sender.messages()) != 1 {
		t.Fatal("a sent mail must not be sent again")
	}
}

func TestMailOutboxRetriesWithBackoffThenGivesUp(t *testing.T) {
	f := newAccountFixture(t)
	f.enqueue(t, "bob@example.com", mailPriorityNotice)
	f.sender.fail = errors.New("421 try\r\nlater")

	f.flush(t)
	r := f.outbox(t, "bob@example.com")
	if r.status != MailStatusPending || r.attempts != 1 || !r.hasBody || r.lastErr != "421 try later" {
		t.Fatalf("after first failure = %+v", r)
	}
	f.flush(t) // 还没到重试时间
	if r := f.outbox(t, "bob@example.com"); r.attempts != 1 {
		t.Fatalf("retried before backoff elapsed: %+v", r)
	}
	for i := 2; i <= mailMaxAttempts; i++ {
		f.clock.advance(mailRetryBase << (i - 1))
		f.flush(t)
	}
	r = f.outbox(t, "bob@example.com")
	if r.status != MailStatusFailed || r.attempts != mailMaxAttempts || r.hasBody {
		t.Fatalf("after %d failures = %+v, want failed with body cleared", mailMaxAttempts, r)
	}
}

func TestMailOutboxHonoursRateAndPriority(t *testing.T) {
	f := newAccountFixture(t)
	if _, err := f.update(t, map[string]any{"mail.ratePerMinute": 2}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	f.enqueue(t, "bulk1@example.com", mailPriorityBulk)
	f.enqueue(t, "bulk2@example.com", mailPriorityBulk)
	f.enqueue(t, "code@example.com", mailPriorityCode)

	f.flush(t) // 一分钟的额度 = 2 封
	msgs := f.sender.messages()
	if len(msgs) != 2 || msgs[0].To != "code@example.com" {
		t.Fatalf("first window sent %+v, want 2 mails with the code mail first", msgs)
	}
	f.flush(t)
	if len(f.sender.messages()) != 3 {
		t.Fatalf("second window total = %d, want 3", len(f.sender.messages()))
	}
}

func TestMailDisabledWhenNoSender(t *testing.T) {
	sf := newSettingsFixture(t)
	mails := NewMailService(sf.pool, testSealer(t), nil, sf.settings, sf.clock.now)
	err := mails.inTx(context.Background(), func(q *sqlcgen.Queries) error {
		return mails.enqueue(context.Background(), q, "a@example.com", mailContent{Subject: "s", Text: "t"}, mailPriorityCode)
	})
	if !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("enqueue without sender = %v, want feature_disabled", err)
	}
	if err := mails.SendTest(context.Background(), sf.actor, "a@example.com", nil); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("SendTest without sender = %v, want feature_disabled", err)
	}
	if n, err := mails.ProcessOutbox(context.Background(), time.Minute); n != 0 || err != nil {
		t.Fatalf("ProcessOutbox without sender = %d, %v", n, err)
	}
}

func TestSendTestMailMapsSMTPFailure(t *testing.T) {
	f := newAccountFixture(t)
	if err := f.mail.SendTest(context.Background(), f.actor, "Admin@Example.com", nil); err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if msgs := f.sender.messages(); len(msgs) != 1 || msgs[0].To != "admin@example.com" {
		t.Fatalf("test mail = %+v", msgs)
	}
	if err := f.mail.SendTest(context.Background(), f.actor, "not-an-email", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad address = %v, want invalid_argument", err)
	}
	f.sender.fail = errors.New("535 authentication failed")
	if err := f.mail.SendTest(context.Background(), f.actor, "admin@example.com", nil); !errors.Is(err, ErrUpstream) {
		t.Fatalf("smtp failure = %v, want upstream_unavailable", err)
	}
}

var sixDigits = regexp.MustCompile(`\b[0-9]{6}\b`)

// lastCode 发掉发件箱并取出发给 to 的最后一封邮件里的验证码。
func (f *accountFixture) lastCode(t *testing.T, to string) string {
	t.Helper()
	f.flush(t)
	msgs := f.sender.messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].To == to {
			if code := sixDigits.FindString(msgs[i].Text); code != "" {
				return code
			}
		}
	}
	t.Fatalf("no code mail for %s in %d messages", to, len(msgs))
	return ""
}

func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestEmailCodeRegisterFlow(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.7")
	if err := f.account.SendEmailCode(ctx, " Alice@Example.com ", EmailCodeRegister, &ip); err != nil {
		t.Fatalf("SendEmailCode: %v", err)
	}
	code := f.lastCode(t, "alice@example.com")
	var stored []byte
	if err := f.pool.QueryRow(ctx, "SELECT code_hash FROM email_code").Scan(&stored); err != nil {
		t.Fatalf("read code hash: %v", err)
	}
	if strings.Contains(string(stored), code) {
		t.Fatal("email code must not be stored in plaintext")
	}
	if _, err := f.account.checkEmailCode(ctx, "alice@example.com", EmailCodeReset, code); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("register code used for reset = %v, want invalid_argument", err)
	}
	if _, err := f.account.checkEmailCode(ctx, "bob@example.com", EmailCodeRegister, code); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("code used for another email = %v, want invalid_argument", err)
	}
	id, err := f.account.checkEmailCode(ctx, "alice@example.com", EmailCodeRegister, code)
	if err != nil {
		t.Fatalf("checkEmailCode: %v", err)
	}
	consume := func() error {
		return f.account.inTx(ctx, func(q *sqlcgen.Queries) error { return f.account.consumeEmailCode(ctx, q, id) })
	}
	if err := consume(); err != nil {
		t.Fatalf("consumeEmailCode: %v", err)
	}
	if err := consume(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("second consume = %v, a code is single use", err)
	}
	if _, err := f.account.checkEmailCode(ctx, "alice@example.com", EmailCodeRegister, code); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("check after consume = %v, want invalid_argument", err)
	}
}

func TestEmailCodeLocksAfterFiveWrongAttemptsAndExpires(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	if err := f.account.SendEmailCode(ctx, "alice@example.com", EmailCodeRegister, nil); err != nil {
		t.Fatalf("SendEmailCode: %v", err)
	}
	code := f.lastCode(t, "alice@example.com")
	for i := 0; i < emailCodeMaxAttempts; i++ {
		if _, err := f.account.checkEmailCode(ctx, "alice@example.com", EmailCodeRegister, wrongCode(code)); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("wrong attempt %d = %v", i, err)
		}
	}
	if _, err := f.account.checkEmailCode(ctx, "alice@example.com", EmailCodeRegister, code); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("right code after %d wrong attempts = %v, want invalid_argument", emailCodeMaxAttempts, err)
	}

	f.clock.advance(2 * time.Minute)
	if err := f.account.SendEmailCode(ctx, "alice@example.com", EmailCodeRegister, nil); err != nil {
		t.Fatalf("second SendEmailCode: %v", err)
	}
	fresh := f.lastCode(t, "alice@example.com")
	f.clock.advance(emailCodeTTL + time.Second)
	if _, err := f.account.checkEmailCode(ctx, "alice@example.com", EmailCodeRegister, fresh); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expired code = %v, want invalid_argument", err)
	}
}

func TestEmailCodeRateLimits(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.7")
	send := func(email string) error { return f.account.SendEmailCode(ctx, email, EmailCodeRegister, &ip) }
	if err := send("alice@example.com"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := send("alice@example.com"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second send within a minute = %v, want rate_limited", err)
	}
	for i := 1; i < emailCodePerHour; i++ {
		f.clock.advance(61 * time.Second)
		if err := send("alice@example.com"); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	f.clock.advance(61 * time.Second)
	if err := send("alice@example.com"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("sixth send within an hour = %v, want rate_limited", err)
	}
	// 同一 IP 每小时 20 次：换邮箱也拦
	for i := emailCodePerHour; i < emailCodePerIPHourly; i++ {
		if err := send(strings.Repeat("u", i) + "@example.com"); err != nil {
			t.Fatalf("send from ip #%d: %v", i+1, err)
		}
	}
	if err := send("another@example.com"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("21st send from one ip = %v, want rate_limited", err)
	}
}

func TestEmailCodeRegisterPreconditions(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	if err := f.account.SendEmailCode(ctx, "alice@example.com", "login", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown purpose = %v", err)
	}
	if err := f.account.SendEmailCode(ctx, "alice@example.com", EmailCodeBind, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bind purpose must not be available to anonymous callers, got %v", err)
	}
	if err := f.account.SendEmailCode(ctx, "not-an-email", EmailCodeRegister, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad email = %v", err)
	}
	if _, err := f.update(t, map[string]any{"register.emailBlock": []string{"spam.example"}, "register.emailAllow": []string{"example.com"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, email := range []string{"a@spam.example", "a@other.example"} {
		if err := f.account.SendEmailCode(ctx, email, EmailCodeRegister, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s = %v, want invalid_argument (domain not allowed)", email, err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO end_user (username, password_hash, email_code, client_cred_enc, email) VALUES ('taken', 'x', 'abc123', '\x00', 'taken@example.com'), ('legacy@example.com', 'x', 'abc124', '\x00', NULL)`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	for _, email := range []string{"taken@example.com", "Legacy@example.com"} {
		if err := f.account.SendEmailCode(ctx, email, EmailCodeRegister, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s = %v, want conflict (email or legacy username taken)", email, err)
		}
	}
	if _, err := f.update(t, map[string]any{"register.mode": RegisterClosed}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := f.account.SendEmailCode(ctx, "new@example.com", EmailCodeRegister, nil); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("register closed = %v, want feature_disabled", err)
	}
}

func TestEmailCodeResetDoesNotRevealExistence(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO end_user (username, password_hash, email_code, client_cred_enc, email) VALUES ('alice', 'x', 'abc123', '\x00', 'alice@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := f.account.SendEmailCode(ctx, "ghost@example.com", EmailCodeReset, nil); err != nil {
		t.Fatalf("reset for unknown email must succeed, got %v", err)
	}
	if err := f.account.SendEmailCode(ctx, "alice@example.com", EmailCodeReset, nil); err != nil {
		t.Fatalf("reset for known email: %v", err)
	}
	f.flush(t)
	msgs := f.sender.messages()
	if len(msgs) != 1 || msgs[0].To != "alice@example.com" {
		t.Fatalf("mails = %+v, want exactly one to the existing user", msgs)
	}
	// 不存在的邮箱也受同样的频控
	if err := f.account.SendEmailCode(ctx, "ghost@example.com", EmailCodeReset, nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second reset for unknown email = %v, want rate_limited", err)
	}
}
