package health

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// APNsNotifier sends an alert notification to one device token.
type APNsNotifier struct {
	KeyPEM      string
	KeyID       string
	TeamID      string
	Topic       string
	DeviceToken string
	Client      *http.Client
}

func (n *APNsNotifier) Name() string { return "apns" }

func (n *APNsNotifier) Notify(ctx context.Context, e LogEntry) error {
	tok, err := apnsJWT(n.KeyPEM, n.KeyID, n.TeamID, time.Now())
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"aps":   map[string]any{"alert": Summarize(e), "sound": "default"},
		"alarm": e.Name, "status": e.Status.String(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.push.apple.com/3/device/"+n.DeviceToken, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", n.Topic)
	req.Header.Set("apns-push-type", "alert")
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("apns: %s %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}

func apnsJWT(keyPEM, keyID, teamID string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return "", fmt.Errorf("apns: key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("apns: key is not ECDSA")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"alg":"ES256","kid":"%s"}`, keyID)))
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iss":"%s","iat":%d}`, teamID, now.Unix())))
	sum := sha256.Sum256([]byte(header + "." + claims))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	rb, sb := r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))
	sig := base64.RawURLEncoding.EncodeToString(append(rb, sb...))
	return header + "." + claims + "." + sig, nil
}

// FCMNotifier posts to the legacy FCM HTTP API.
type FCMNotifier struct {
	ServerKey string
	Token     string
	Client    *http.Client
}

func (n *FCMNotifier) Name() string { return "fcm" }

func (n *FCMNotifier) Notify(ctx context.Context, e LogEntry) error {
	body, _ := json.Marshal(map[string]any{
		"to":           n.Token,
		"notification": map[string]string{"title": e.Name, "body": Summarize(e)},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fcm.googleapis.com/fcm/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "key="+n.ServerKey)
	req.Header.Set("Content-Type", "application/json")
	return doNotify(ctx, n.Client, req)
}

// HuaweiNotifier posts a downlink to the Huawei Push Kit when a bearer token is configured.
type HuaweiNotifier struct {
	AppID  string
	Token  string
	RegID  string
	Client *http.Client
}

func (n *HuaweiNotifier) Name() string { return "huawei" }

func (n *HuaweiNotifier) Notify(ctx context.Context, e LogEntry) error {
	body, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"token":        []string{n.RegID},
			"notification": map[string]string{"title": e.Name, "body": Summarize(e)},
		},
	})
	url := "https://push-api.cloud.huawei.com/v1/" + url.PathEscape(n.AppID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("Content-Type", "application/json")
	return doNotify(ctx, n.Client, req)
}

// XiaomiNotifier posts to the Xiaomi push regid endpoint.
type XiaomiNotifier struct {
	AppSecret string
	Package   string
	RegID     string
	Client    *http.Client
}

func (n *XiaomiNotifier) Name() string { return "xiaomi" }

func (n *XiaomiNotifier) Notify(ctx context.Context, e LogEntry) error {
	form := url.Values{}
	form.Set("registration_id", n.RegID)
	form.Set("restricted_package_name", n.Package)
	form.Set("title", e.Name)
	form.Set("description", Summarize(e))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.xmpush.xiaomi.com/v3/message/regid", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "key="+n.AppSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doNotify(ctx, n.Client, req)
}

// SMSNotifier sends an Aliyun SMS or posts to a generic gateway URL.
type SMSNotifier struct {
	Provider  string
	AccessKey string
	Secret    string
	SignName  string
	Template  string
	Phone     string
	URL       string
	Client    *http.Client
}

func (n *SMSNotifier) Name() string { return "sms" }

func (n *SMSNotifier) Notify(ctx context.Context, e LogEntry) error {
	if strings.EqualFold(n.Provider, "aliyun") {
		return n.aliyun(ctx, e)
	}
	if n.URL == "" {
		return fmt.Errorf("sms: url required")
	}
	body, _ := json.Marshal(map[string]string{"phone": n.Phone, "text": Summarize(e)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doNotify(ctx, n.Client, req)
}

func (n *SMSNotifier) aliyun(ctx context.Context, e LogEntry) error {
	params := map[string]string{
		"AccessKeyId":      n.AccessKey,
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     n.Phone,
		"RegionId":         "cn-hangzhou",
		"SignName":         n.SignName,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   fmt.Sprintf("%d", time.Now().UnixNano()),
		"SignatureVersion": "1.0",
		"TemplateCode":     n.Template,
		"TemplateParam":    fmt.Sprintf(`{"text":%q}`, e.Name),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}
	params["Signature"] = aliyunSign("GET", n.Secret, params)
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://dysmsapi.aliyuncs.com/?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	return doNotify(ctx, n.Client, req)
}

func aliyunSign(method, secret string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canon strings.Builder
	for i, k := range keys {
		if i > 0 {
			canon.WriteByte('&')
		}
		canon.WriteString(aliyunEscape(k))
		canon.WriteByte('=')
		canon.WriteString(aliyunEscape(params[k]))
	}
	stringToSign := method + "&" + aliyunEscape("/") + "&" + aliyunEscape(canon.String())
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func aliyunEscape(s string) string {
	esc := url.QueryEscape(s)
	esc = strings.ReplaceAll(esc, "+", "%20")
	esc = strings.ReplaceAll(esc, "*", "%2A")
	esc = strings.ReplaceAll(esc, "%7E", "~")
	return esc
}

func doNotify(ctx context.Context, c *http.Client, req *http.Request) error {
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("notify: %s %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}
