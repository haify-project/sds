package metrics

import (
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// ==================== Wholesale-replaced gauge vectors ====================

// seriesSet is a GaugeVec whose entire contents are replaced each collection
// cycle, remembering which label sets it currently exports so the ones that
// disappear can be deleted individually.
//
// GaugeVec.Reset() before repopulating would be one line instead of this, but
// it empties the vector for the whole rebuild. A scrape landing in that window
// sees no pools and no replicas at all — indistinguishable from a cluster that
// has none, which is the exact class of lie this package exists to avoid.
// Deleting only the vanished series never has such a window.
type seriesSet struct {
	vec *prometheus.GaugeVec
	// live maps a canonical label key to the labels themselves, for the series
	// currently exported.
	live map[string]prometheus.Labels
	// next accumulates the labels written during the cycle in progress; nil
	// outside a begin/commit pair.
	next map[string]prometheus.Labels
}

func newSeriesSet(vec *prometheus.GaugeVec) *seriesSet {
	return &seriesSet{vec: vec, live: map[string]prometheus.Labels{}}
}

// begin starts a replacement cycle.
func (s *seriesSet) begin() {
	s.next = make(map[string]prometheus.Labels, len(s.live))
}

// set writes one series and marks it as surviving the cycle in progress.
func (s *seriesSet) set(labels prometheus.Labels, value float64) {
	s.vec.With(labels).Set(value)
	if s.next != nil {
		s.next[labelKey(labels)] = labels
	}
}

// commit deletes every series that was exported before this cycle and was not
// written during it.
func (s *seriesSet) commit() {
	for key, labels := range s.live {
		if _, kept := s.next[key]; !kept {
			s.vec.Delete(labels)
		}
	}
	s.live = s.next
	s.next = nil
}

// reset drops every series, used only by ResetMetrics.
func (s *seriesSet) reset() {
	s.vec.Reset()
	s.live = map[string]prometheus.Labels{}
	s.next = nil
}

// labelKey renders a label set as a comparable string. Label names are sorted
// so the key does not depend on map iteration order.
func labelKey(labels prometheus.Labels) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(labels[name])
		b.WriteByte(0x1f) // unit separator: cannot appear in a label value
	}
	return b.String()
}
