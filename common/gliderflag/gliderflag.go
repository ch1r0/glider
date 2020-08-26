package gliderflag

import (
	"github.com/nadoo/conflag"
	"github.com/nadoo/glider/common/timewindow"
)

// GliderFlag - custom flag class with additional methods
type GliderFlag struct {
	*conflag.Conflag
}

type timeWindowSliceValue struct {
	value   *[]timewindow.TimeWindow
	changed bool
}

// New parses os args and returns a new GliderFlag instance.
func New() *GliderFlag {
	return &GliderFlag{Conflag: conflag.New()}
}

// NewFromFile parses cfgFile and returns a new GliderFlag instance.
func NewFromFile(app, cfgFile string) *GliderFlag {
	return &GliderFlag{Conflag: conflag.NewFromFile(app, cfgFile)}
}

func (s *timeWindowSliceValue) Set(val string) error {
	if !s.changed {
		*s.value = []timewindow.TimeWindow{timewindow.Parse(val)}
		s.changed = true
	} else {
		*s.value = append(*s.value, timewindow.Parse(val))
	}
	return nil
}

func (s *timeWindowSliceValue) String() string {
	return ""
}

// TimeWindowSliceVar - config a time window
func (f *GliderFlag) TimeWindowSliceVar(pValue *[]timewindow.TimeWindow, name string, defaultValue []timewindow.TimeWindow, usage string) {
	timeWindowSlice := &timeWindowSliceValue{value: pValue}
	*timeWindowSlice.value = defaultValue
	f.Var(timeWindowSlice, name, usage)
}
