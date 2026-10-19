// Package metricstest reads a Prometheus text exposition back into samples,
// for tests that scrape /metrics and assert on series.
package metricstest

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/metrics"
)

// Sample is one series: its labels and value.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Scrape is a parsed exposition: samples by series name (a histogram's
// series are name_bucket, name_sum and name_count).
type Scrape struct {
	Samples map[string][]Sample
	Text    string
}

var line = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})?\s+(\S+)(?:\s+\d+)?$`)
var pair = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)

// Parse reads the text format.
func Parse(t testing.TB, r io.Reader) Scrape {
	t.Helper()
	s := Scrape{Samples: map[string][]Sample{}}
	var text strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		text.WriteString(l + "\n")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		m := line.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("metricstest: cannot parse %q", l)
		}
		v, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			t.Fatalf("metricstest: value in %q: %v", l, err)
		}
		labels := map[string]string{}
		for _, p := range pair.FindAllStringSubmatch(m[2], -1) {
			labels[p[1]] = p[2]
		}
		s.Samples[m[1]] = append(s.Samples[m[1]], Sample{Labels: labels, Value: v})
	}
	s.Text = text.String()
	return s
}

// Fetch scrapes url.
func Fetch(t testing.TB, url string) Scrape {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, res.StatusCode)
	}
	return Parse(t, res.Body)
}

// Gather scrapes m through its handler.
func Gather(t testing.TB, m *metrics.Metrics) Scrape {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	return Fetch(t, srv.URL+"/metrics")
}

// match: the sample has every label in kv (name, value, name, value...).
func match(s Sample, kv []string) bool {
	for i := 0; i+1 < len(kv); i += 2 {
		if s.Labels[kv[i]] != kv[i+1] {
			return false
		}
	}
	return true
}

// Sum adds up the series name whose labels include kv.
func (s Scrape) Sum(name string, kv ...string) float64 {
	var sum float64
	for _, x := range s.Samples[name] {
		if match(x, kv) {
			sum += x.Value
		}
	}
	return sum
}

// Has reports whether a series name with the labels kv exists.
func (s Scrape) Has(name string, kv ...string) bool {
	for _, x := range s.Samples[name] {
		if match(x, kv) {
			return true
		}
	}
	return false
}

// Series counts the series of the metric names starting with prefix.
func (s Scrape) Series(prefix string) int {
	n := 0
	for name, xs := range s.Samples {
		if strings.HasPrefix(name, prefix) {
			n += len(xs)
		}
	}
	return n
}

// LabelValues lists the distinct values of label across the series name.
func (s Scrape) LabelValues(name, label string) []string {
	seen := map[string]bool{}
	for _, x := range s.Samples[name] {
		if v, ok := x.Labels[label]; ok {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
