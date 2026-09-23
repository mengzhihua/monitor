package health

import "testing"

func TestAliyunSignStable(t *testing.T) {
	params := map[string]string{
		"AccessKeyId": "id",
		"Action":      "SendSms",
		"Format":      "JSON",
	}
	a := aliyunSign("GET", "secret", params)
	b := aliyunSign("GET", "secret", params)
	if a == "" || a != b {
		t.Fatalf("%q %q", a, b)
	}
	if aliyunSign("POST", "secret", params) == a {
		t.Fatal("method should change the signature")
	}
}
