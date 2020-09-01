package primarydomains

// Use whois.domaintool.com to determine the primary domain name of a host. E.g. www.google.com -> google.com
// Check known TLDs first before calling whois.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nadoo/glider/common/log"
)

var redirectRegex = regexp.MustCompile("https{0,1}://whois\\.domaintools\\.com/(.*)")

// Lookup handles primary domain lookup
type Lookup struct {
	tlds       []map[string]bool
	cacheFile  string
	cacheMutex *sync.RWMutex
	cacheDirty int32
	cache      map[string]string
	stop       chan bool
}

// NewLookup instantiates a new primary domain lookup
func NewLookup(tlds []string, cacheFile string) *Lookup {
	lookup := &Lookup{
		tlds:       make([]map[string]bool, 3),
		cacheFile:  cacheFile,
		cacheMutex: &sync.RWMutex{},
		cacheDirty: 0,
		cache:      make(map[string]string),
		stop:       make(chan bool),
	}

	// Save top-level domains based on number of segments
	for _, domain := range tlds {
		dotCount := strings.Count(domain, ".")
		if lookup.tlds[dotCount] == nil {
			lookup.tlds[dotCount] = make(map[string]bool)
		}
		lookup.tlds[dotCount][domain] = true
	}

	lookup.loadCache()

	// Start a goroutine to flush the cache periodically
	go lookup.flushCacheRoutine(30 * time.Second)

	return lookup
}

func (lookup *Lookup) loadCache() {
	if lookup.cacheFile == "" {
		log.F("[primarydomain] loadCache: cacheFile not specified")
		return
	}

	jsonString, err := os.ReadFile(lookup.cacheFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.F("[primarydomain] cache does not exist - create new")
			// will be created later
			return
		}

		log.Fatalf("[primarydomain] Error reading cache [%s] - %s", lookup.cacheFile, err.Error())
	}

	err = json.Unmarshal(jsonString, &lookup.cache)
	if err != nil {
		log.Fatalf("[primarydomain] Error deserializing cache - %s", err.Error())
	}

	log.F("[primarydomain] Loaded %d records", len(lookup.cache))
}

func (lookup *Lookup) flushCacheRoutine(interval time.Duration) {
	log.F("[primarydomain] Start flushCacheRoutine")
	for {
		lookup.flushCache()
		select {
		case <-time.After(interval): // Wait until interval elapses
		case <-lookup.stop: // Or stop signal received
			return // in which case terminate this goroutine
		}
	}
}

func (lookup *Lookup) flushCache() {
	if lookup.cacheFile == "" {
		log.F("[primarydomain] flushCache: cacheFile not specified")
		return
	}

	if !atomic.CompareAndSwapInt32(&lookup.cacheDirty, 1, 0) {
		// Not dirty
		return
	}

	log.F("[primarydomain] flushCache")
	lookup.cacheMutex.RLock()
	defer lookup.cacheMutex.RUnlock()

	jsonString, err := json.Marshal(lookup.cache)
	if err != nil {
		log.F("[primarydomain] Error serializing cache - " + err.Error())
		return
	}

	log.F("%s", jsonString)
	os.WriteFile(lookup.cacheFile, jsonString, 0644)
}

func (lookup *Lookup) matchByKnownTlds(hostname string) string {
	// E.g. if input is www.company.com and "com" is a known TLD, return "company.com"
	tokens := strings.Split(hostname, ".")
	for i := 0; i < len(tokens); i++ {
		segments := len(tokens) - i
		if segments > len(lookup.tlds) {
			continue
		}

		domain := strings.Join(tokens[i:], ".")
		if lookup.tlds[segments-1][domain] {
			if i == 0 {
				return hostname
			}
			return strings.Join(tokens[i-1:], ".")
		}
	}

	return ""
}

// Get returns the primary domain for an URL
func (lookup *Lookup) Get(requestURL string) (string, error) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return "", err
	}

	hostname := u.Hostname()
	domain := lookup.matchByKnownTlds(hostname)

	if domain != "" {
		// No need to cache if matched with known TLD
		log.F("[primarydomain] matchByKnownTlds [%s] returned [%s]", hostname, domain)
		return domain, nil
	}

	lookup.cacheMutex.RLock()
	domain = lookup.cache[hostname]
	lookup.cacheMutex.RUnlock()

	if domain != "" {
		return domain, nil
	}

	// Do not redirect
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Request
	req, err := http.NewRequest("GET", "https://whois.domaintools.com/go/?q="+url.QueryEscape(hostname)+"&service=whois", nil)
	if err != nil {
		return "", err
	}

	// Must assign user agent. Otherwise HTTP 500.
	req.Header.Add("User-Agent", "Glider")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 300 && resp.StatusCode != 302 {
		// Something went wrong
		return "", errors.New("domaintools.com returned HTTP " + resp.Status)
	}

	if resp.StatusCode == 302 {
		if resp.Header["Location"] == nil || len(resp.Header["Location"]) != 1 {
			return "", errors.New("domaintools.com returned HTTP 302 without proper Location header")
		}

		redirectURL := resp.Header["Location"][0]
		if !redirectRegex.MatchString(redirectURL) {
			return "", errors.New("domaintools.com redirected unsupported URL [" + redirectURL + "]")
		}

		matches := redirectRegex.FindStringSubmatch(redirectURL)
		domain = matches[1]
		log.F("[primarydomain] whois query for [%s] redirected to [%s]", hostname, domain)
	} else {
		// 20x - no redirection - should not have happened - let's assume the host name is already the primary domain
		domain = hostname
		log.F("[primarydomain] whois query for [%s] got no redirection")
	}

	lookup.cacheMutex.Lock()
	lookup.cache[hostname] = domain
	lookup.cacheMutex.Unlock()

	atomic.StoreInt32(&lookup.cacheDirty, 1)
	return domain, nil
}
