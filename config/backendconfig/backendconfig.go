package backendconfig

var Default = Config{
	Monerod:  "127.0.0.1:18081",
	DevXMR0:  "127.0.0.1:28088",
	TestXMR0: "127.0.0.1:28089",
	TestXMR1: "127.0.0.1:28090",
}

type Config struct {
	Monerod  string
	DevXMR0  string
	TestXMR0 string
	TestXMR1 string
}
