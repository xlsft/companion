package contracts

// Platform bundles the bridges for the running OS. Any accessor may return
// nil when the capability report marks it unavailable; callers check the
// report rather than probing for nil.
type Platform interface {
	Detector() CapabilityDetector
	Displays() DisplayService
	Hotkeys() HotkeyService
	Capturer() ScreenCapturer
	Overlay() OverlayHost
	Input() InputInjector
	Accessibility() Accessibility
	Secrets() SecretStore
}
