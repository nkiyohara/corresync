package windowszone

// IANA returns Unicode CLDR's territory-neutral IANA identifier for a Windows
// time-zone identifier. Territory-neutral mappings are deterministic when an
// account's territory is unavailable, as it is for a task-only Graph grant.
func IANA(windowsID string) (string, bool) {
	iana, ok := windowsToIANA[windowsID]
	return iana, ok
}
