package quic

import "fmt"

type ATSSSsteeingMode int

const (
	ActiveStandy ATSSSsteeingMode = iota
	SmallestDelay
)

// How many time (millisecond) to do a test.
const testFrequency int = 1000

const ATSSSLog bool = false

func ATSSSPrintf(format string, args ...interface{}) {
	if ATSSSLog {
		fmt.Printf(format, args...)
	}
}

func ATSSSPrintln(s string) {
	if ATSSSLog {
		fmt.Println(s)
	}
}
