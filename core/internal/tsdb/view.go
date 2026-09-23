package tsdb

// Reader is the query surface the API needs for one host's series. *Store
// implements it directly; Prefixed exposes a remote node's series stored in
// the same Store under a key prefix.
type Reader interface {
	Bounds(id string) (first, last int64, ok bool)
	QueryTier(id string, tier int, after, before int64) ([]Bucket, error)
	QueryAggregated(id string, tier int, after, before int64, points int, fn GroupFunc) (Result, error)
	TierCovers(id string, tier int, after int64) bool
	PlanTier(after, before int64, points int) int
	TierEvery(tier int) (int64, bool)
}

// Prefixed returns a Reader/Sink view of s where every series id is
// namespaced with prefix.
func Prefixed(s *Store, prefix string) *View {
	return &View{s: s, prefix: prefix}
}

type View struct {
	s      *Store
	prefix string
}

func (v *View) Append(id string, ts int64, val float64) { v.s.Append(v.prefix+id, ts, val) }
func (v *View) Bounds(id string) (int64, int64, bool)   { return v.s.Bounds(v.prefix + id) }
func (v *View) QueryTier(id string, tier int, after, before int64) ([]Bucket, error) {
	return v.s.QueryTier(v.prefix+id, tier, after, before)
}
func (v *View) QueryAggregated(id string, tier int, after, before int64, points int, fn GroupFunc) (Result, error) {
	return v.s.QueryAggregated(v.prefix+id, tier, after, before, points, fn)
}
func (v *View) TierCovers(id string, tier int, after int64) bool {
	return v.s.TierCovers(v.prefix+id, tier, after)
}
func (v *View) PlanTier(after, before int64, points int) int {
	return v.s.PlanTier(after, before, points)
}
func (v *View) TierEvery(tier int) (int64, bool) { return v.s.TierEvery(tier) }
