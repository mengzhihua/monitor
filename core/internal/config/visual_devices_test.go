package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const deviceVisualFixture = `collectors:
  modules:
    snmp:
      command: /usr/local/bin/snmpwalk
      targets:
        - name: switch-1
          address: 192.0.2.1
          profile: network
          version: 3
          user: monitor
          auth_protocol: SHA-256
          auth_passphrase: " keep authentication spaces "
          priv_protocol: AES
          priv_passphrase: private-example
          timeout: 3s
          items:
            - name: custom_sensor
              oid: .1.3.6.1.4.1.99999.1
          lld:
            - name: custom_discovery
              table: .1.3.6.1.4.1.99999.2
              value: .1.3.6.1.4.1.99999.3
        - name: ups-1
          address: 192.0.2.2
          profile: ups
          version: 2c
          community: community-example
    redfish:
      targets:
        - name: server-1
          url: https://192.0.2.3/redfish/v1
          user: monitor
          password: " keep password spaces "
          tls:
            ca_file: /etc/monitor/bmc-ca.pem
            insecure_skip_verify: false
            future_option: retain-me
          future_device_setting:
            nested: retain-this-too
`

func moduleMap(t *testing.T, raw string, module string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	return document["collectors"].(map[string]any)["modules"].(map[string]any)[module].(map[string]any)
}

func TestVisualDeviceListsPreserveSecretsAndUnknownFields(t *testing.T) {
	v, err := VisualFrom(deviceVisualFixture)
	if err != nil {
		t.Fatal(err)
	}
	if v.SNMPLegacy || v.RedfishLegacy || v.SNMPTargets == nil || v.RedfishTargets == nil {
		t.Fatal("named lists not exposed")
	}
	if len(*v.SNMPTargets) != 2 || (*v.SNMPTargets)[0].AuthProtocol != "SHA-256" || (*v.RedfishTargets)[0].TLS.CAFile == nil {
		t.Fatal("device settings missing")
	}
	// Exercise the real JSON boundary. Omitted credentials must retain the YAML
	// value even when an API client submits only the device's visible settings.
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var submitted Visual
	if err := json.Unmarshal(encoded, &submitted); err != nil {
		t.Fatal(err)
	}
	(*submitted.SNMPTargets)[0].AuthPassphrase = nil
	(*submitted.SNMPTargets)[0].PrivPassphrase = nil
	(*submitted.RedfishTargets)[0].Password = nil
	(*submitted.RedfishTargets)[0].TLS = nil
	submitted.Hostname = "unrelated-host-edit"
	next, err := ApplyVisual(deviceVisualFixture, submitted)
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"snmp", "redfish"} {
		before, after := moduleMap(t, deviceVisualFixture, module), moduleMap(t, next, module)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("%s module changed during unrelated edit", module)
		}
	}
}

func TestVisualDeviceListUpdatesOneTargetAndKeepsAdvancedFields(t *testing.T) {
	v, err := VisualFrom(deviceVisualFixture)
	if err != nil {
		t.Fatal(err)
	}
	(*v.SNMPTargets)[0].Address = "192.0.2.10"
	*v.SNMPTargets = append((*v.SNMPTargets)[:1], VisualSNMPTarget{Name: "printer-1", Address: "192.0.2.4", Profile: "printer", Version: "2c", Community: deviceString("readonly")})
	(*v.RedfishTargets)[0].Password = deviceString("new-example-password")
	next, err := ApplyVisual(deviceVisualFixture, v)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"custom_sensor", "custom_discovery", "/usr/local/bin/snmpwalk", "192.0.2.10", "printer-1", "new-example-password", "future_option", "future_device_setting"} {
		if !strings.Contains(next, keep) {
			t.Fatalf("missing retained or updated field %s", keep)
		}
	}
	if strings.Contains(next, "ups-1") {
		t.Fatal("removed target retained")
	}
	// Explicitly empty lists stay empty; removing the targets key could silently
	// reactivate a legacy localhost target on collectors with singleton defaults.
	*v.SNMPTargets = []VisualSNMPTarget{}
	next, err = ApplyVisual(next, v)
	if err != nil {
		t.Fatal(err)
	}
	if targets := moduleMap(t, next, "snmp")["targets"].([]any); len(targets) != 0 {
		t.Fatal("expected explicit empty list")
	}
}

func TestVisualDeviceLegacyIsUntouchedAndCannotBeSilentlyConverted(t *testing.T) {
	for _, tc := range []struct{ module, config string }{
		{"snmp", "address: 192.0.2.1\n      community: legacy-example\n      items: [{name: sensor, oid: .1.2.3}]"},
		{"redfish", "url: https://192.0.2.2/redfish/v1\n      password: legacy-example\n      tls: {ca_file: /private/ca.pem}"},
	} {
		t.Run(tc.module, func(t *testing.T) {
			raw := fmt.Sprintf("collectors:\n  modules:\n    %s:\n      %s\n", tc.module, tc.config)
			v, err := VisualFrom(raw)
			if err != nil {
				t.Fatal(err)
			}
			if v.SNMPTargets != nil || v.RedfishTargets != nil || !(v.SNMPLegacy || v.RedfishLegacy) {
				t.Fatal("legacy marker/pointer mismatch")
			}
			v.Hostname = "safe-edit"
			next, err := ApplyVisual(raw, v)
			if err != nil || !reflect.DeepEqual(moduleMap(t, raw, tc.module), moduleMap(t, next, tc.module)) {
				t.Fatalf("legacy module modified: %v", err)
			}
			if tc.module == "snmp" {
				v.SNMPTargets = &[]VisualSNMPTarget{{Name: "new", Address: "192.0.2.5"}}
			} else {
				v.RedfishTargets = &[]VisualRedfishTarget{{Name: "new", URL: "https://192.0.2.5/redfish/v1"}}
			}
			if _, err := ApplyVisual(raw, v); err == nil || !strings.Contains(err.Error(), "legacy") {
				t.Fatalf("silent legacy conversion accepted: %v", err)
			}
		})
	}
}

func TestVisualDeviceTargetValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Visual)
	}{
		{"duplicate name", func(v *Visual) { (*v.SNMPTargets)[1].Name = "switch-1" }},
		{"unsafe name", func(v *Visual) { (*v.SNMPTargets)[0].Name = "bad.name" }},
		{"no silent rename", func(v *Visual) { (*v.SNMPTargets)[0].Name = " switch-1 " }},
		{"empty address", func(v *Visual) { (*v.SNMPTargets)[0].Address = "" }},
		{"option address", func(v *Visual) { (*v.SNMPTargets)[0].Address = "-v3" }},
		{"unsupported profile", func(v *Visual) { (*v.SNMPTargets)[0].Profile = "all-vendors" }},
		{"version", func(v *Visual) { (*v.SNMPTargets)[0].Version = "4" }},
		{"missing community", func(v *Visual) { (*v.SNMPTargets)[1].Community = deviceString("") }},
		{"missing v3 user", func(v *Visual) { (*v.SNMPTargets)[0].User = "" }},
		{"privacy without authentication", func(v *Visual) { (*v.SNMPTargets)[0].AuthPassphrase = deviceString("") }},
		{"timeout", func(v *Visual) { (*v.SNMPTargets)[0].Timeout = "2m" }},
		{"url credentials", func(v *Visual) { (*v.RedfishTargets)[0].URL = "https://admin:secret@example/redfish/v1" }},
		{"url scheme", func(v *Visual) { (*v.RedfishTargets)[0].URL = "file:///etc/passwd" }},
		{"url query", func(v *Visual) { (*v.RedfishTargets)[0].URL = "https://example/redfish/v1?token=secret" }},
		{"device limit", func(v *Visual) { list := make([]VisualSNMPTarget, 65); v.SNMPTargets = &list }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := VisualFrom(deviceVisualFixture)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&v)
			if _, err := ApplyVisual(deviceVisualFixture, v); err == nil {
				t.Fatal("invalid device accepted")
			}
		})
	}
}

func TestVisualDeviceSecretCanBeExplicitlyCleared(t *testing.T) {
	v, err := VisualFrom(deviceVisualFixture)
	if err != nil {
		t.Fatal(err)
	}
	(*v.RedfishTargets)[0].Password = deviceString("")
	next, err := ApplyVisual(deviceVisualFixture, v)
	if err != nil {
		t.Fatal(err)
	}
	device := moduleMap(t, next, "redfish")["targets"].([]any)[0].(map[string]any)
	if _, exists := device["password"]; exists {
		t.Fatal("explicit password removal ignored")
	}
}

func TestParseValidatesNamedDevicesWithoutContactingTargets(t *testing.T) {
	valid := `collectors:
  modules:
    snmp:
      targets:
        - name: unreachable
          address: 192.0.2.10
          profile: host
          version: 2c
          community: test-example
    redfish:
      targets:
        - name: unreachable-bmc
          url: https://192.0.2.20/redfish/v1
          user: monitor
          password: test-example
          tls:
            ca_file: /does/not/exist/on/config/editor.pem
`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("config validation must not open CA files or contact devices: %v", err)
	}
	for _, tc := range []struct{ name, old, next string }{
		{"version", "version: 2c", "version: 4"},
		{"name", "name: unreachable\n", "name: bad.name\n"},
		{"nested list", "profile: host", "profile: host\n          targets: [{name: nested, address: 192.0.2.3}]"},
		{"missing community", "community: test-example", "community: ''"},
		{"redfish protocol", "https://192.0.2.20/redfish/v1", "file:///etc/passwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(valid, tc.old, tc.next, 1))); err == nil {
				t.Fatal("invalid named target configuration accepted")
			} else if strings.Contains(err.Error(), "test-example") {
				t.Fatal("validation error exposed credentials")
			}
		})
	}
	// Existing singleton settings retain their startup-time validation behavior.
	legacy := "collectors:\n  modules:\n    snmp:\n      address: 192.0.2.1\n    redfish:\n      url: https://192.0.2.2/redfish/v1\n"
	if _, err := Parse([]byte(legacy)); err != nil {
		t.Fatalf("legacy config rejected: %v", err)
	}
}

func deviceString(s string) *string { return &s }
