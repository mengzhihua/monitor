package collect

import "fmt"

// ValidateDeviceTargets validates explicit device fleets without contacting any
// target or loading credentials/TLS files. Legacy singleton modules keep their
// existing startup validation. Decode errors are deliberately value-free.
func ValidateDeviceTargets(module string, decode func(any) error) error {
	switch module {
	case "snmp":
		var cfg snmpConfig
		if err := decode(&cfg); err != nil {
			return fmt.Errorf("snmp: invalid target configuration")
		}
		if cfg.Targets == nil {
			return nil
		}
		return (&snmpCollector{cfg: cfg}).Configure(func(any) error { return nil })
	case "redfish":
		var cfg redfishConfig
		if err := decode(&cfg); err != nil {
			return fmt.Errorf("redfish: invalid target configuration")
		}
		if cfg.Targets == nil {
			return nil
		}
		return (&redfishCollector{cfg: cfg}).Configure(func(any) error { return nil })
	}
	return nil
}
