package timewindow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nadoo/glider/common/log"
)

// TimeWindow - a time window
type TimeWindow struct {
	FromDay  int // 1 - Monday, 7 = Sunday, inclusive
	ToDay    int // 1 - Monday, 7 = Sunday, inclusive
	FromHour int // 0 - midnight, 12 = noon, 23 = 11PM
	FromMin  int
	ToHour   int // 0 - midnight, 12 = noon, 23 = 11PM
	ToMin    int
}

func parseDaysOfWeek(daysOfWeekString string) (int, int) {
	switch strings.ToUpper(daysOfWeekString) {
	case "MON":
		return 1, 1
	case "TUE":
		return 2, 2
	case "WED":
		return 3, 3
	case "THU":
		return 4, 4
	case "FRI":
		return 5, 5
	case "SAT":
		return 6, 6
	case "SUN":
		return 7, 7
	default:
		daysOfWeek := strings.Split(daysOfWeekString, "-")
		if len(daysOfWeek) != 2 || len(daysOfWeek[0]) != 1 || len(daysOfWeek[1]) != 1 {
			log.Fatal(errors.New("ERROR: invalid days-of-week [" + daysOfWeekString + "]"))
		}
		from, err := strconv.Atoi(daysOfWeek[0])
		if err != nil {
			log.Fatal(err)
		}

		to, err := strconv.Atoi(daysOfWeek[1])
		if err != nil {
			log.Fatal(err)
		}

		if from < 1 || from > 7 || to < 1 || to > 7 {
			log.Fatal(errors.New("ERROR: invalid days-of-week [" + daysOfWeekString + "]"))
		}
		return from, to
	}
}

func parseTime(timeString string) (int, int) {
	timeTokens := strings.Split(timeString, ":")
	if len(timeTokens) != 2 || len(timeTokens[0]) == 0 || len(timeTokens[0]) > 2 || len(timeTokens[1]) == 0 || len(timeTokens[1]) > 2 {
		log.Fatal(errors.New("ERROR: invalid time [" + timeString + "], expected format: HH:MM"))
	}

	hour, err := strconv.Atoi(timeTokens[0])
	if err != nil {
		log.Fatal(err)
	}

	min, err := strconv.Atoi(timeTokens[1])
	if err != nil {
		log.Fatal(err)
	}

	if hour < 0 || hour > 23 || min < 0 || min > 59 {
		log.Fatal(errors.New("ERROR: invalid time [" + timeString + "], expected format: HH:MM. HH: 0 - 23, MM: 0 - 59."))
	}

	return hour, min
}

// Contains check if a specific timestamp is contained in the window
func (timeWindow *TimeWindow) Contains(time time.Time) bool {
	weekday := int(time.Weekday())
	if weekday == 0 {
		weekday = 7
	}

	if weekday < timeWindow.FromDay || weekday > timeWindow.ToDay {
		return false
	}

	if time.Hour() < timeWindow.FromHour || time.Hour() > timeWindow.ToHour {
		return false
	}

	if time.Hour() == timeWindow.FromHour && time.Minute() < timeWindow.FromMin {
		return false
	}

	if time.Hour() == timeWindow.ToHour && time.Minute() > timeWindow.ToMin {
		return false
	}

	return true
}

// String convert value to string
func (timeWindow *TimeWindow) String() string {
	dayOfWeekNames := []string{"XXX", "MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN"}
	var daysOfWeekString string
	if timeWindow.FromDay == timeWindow.ToDay {
		daysOfWeekString = dayOfWeekNames[timeWindow.FromDay]
	} else {
		daysOfWeekString = fmt.Sprintf("%d-%d", timeWindow.FromDay, timeWindow.ToDay)
	}

	return fmt.Sprintf("%s %02d:%02d %02d:%02d", daysOfWeekString, timeWindow.FromHour, timeWindow.FromMin, timeWindow.ToHour, timeWindow.ToMin)
}

// Parse string value
func Parse(timeWindowString string) TimeWindow {
	fields := strings.Fields(timeWindowString)
	if len(fields) != 3 {
		log.Fatal(errors.New("ERROR: invalid time window [" + timeWindowString + "]"))
	}
	timeWindow := TimeWindow{}
	timeWindow.FromDay, timeWindow.ToDay = parseDaysOfWeek(fields[0])
	timeWindow.FromHour, timeWindow.FromMin = parseTime(fields[1])
	timeWindow.ToHour, timeWindow.ToMin = parseTime(fields[2])
	return timeWindow
}
