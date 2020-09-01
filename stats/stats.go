package stats

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/nadoo/glider/common/log"
	"github.com/nadoo/glider/primarydomains"
)

// Session TBD
type Session struct {
	startTime time.Time
	server    net.Conn
	forwarder net.Conn
	url       string
}

// DomainStats TBD
type DomainStats struct {
	settled time.Duration
	active  []Session
}

// Stats TBD
type Stats struct {
	file         string
	domainLookup *primarydomains.Lookup
	mutex        *sync.RWMutex
	statsByDay   map[time.Time]map[string]*DomainStats // date -> daily stats (domain -> domain stats)
}

// NewStats TBD
func NewStats(statsFile string, domainLookup *primarydomains.Lookup) *Stats {
	stats := &Stats{
		file:         statsFile,
		domainLookup: domainLookup,
		mutex:        &sync.RWMutex{},
	}

	stats.load()
	return stats
}

func (stats *Stats) load() {
	jsonString, err := os.ReadFile(stats.file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.F("[stats] stats does not exist - create new")
			// will be created later
			return
		}
		log.Fatalf("[stats] Error reading [%s] - %s", stats.file, err.Error())
	}

	err = json.Unmarshal(jsonString, &stats.statsByDay)
	if err != nil {
		log.Fatalf("[stats] Error deserializing json - %s", err.Error())
	}

	// Wipe out active connections after a restart
	for _, dailyStats := range stats.statsByDay {
		for _, domainStats := range dailyStats {
			domainStats.active = nil
		}
	}
}
