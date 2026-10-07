package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Station names. A station shows the first of these that exists: the name its operator chose when minting
// its token, the vessel name the Signal K plugin sent, its own vessel's name from !AIVDO, then "near" plus its
// coverage label. All of it is kept by base station id (the part before any "/"), in memory and, when the
// vessel record is attached, in its stations table, so names survive deploys.

const (
	ownSettle      = time.Hour // two own MMSIs this close together name nothing until one has had the hour alone
	nameMaxRunes   = 40
	nameChangesDay = 10 // signed name changes per station per day
	mintSkew       = 5 * time.Minute
	labelEvery     = 10 // label passes run on every tenth minute tick
)

// stationMeta is what the server knows about a station beyond its traffic.
type stationMeta struct {
	Near       string // coverage label: the town or region nearest the traffic it hears
	Own        uint32 // its own vessel, decided from its !AIVDO; 0 = none, or two in the last ownSettle
	Name       string // chosen by the operator at mint
	VesselName string // the boat's name, sent by the Signal K plugin at mint
	SignedTS   int64  // ts of the last accepted signed mint: a signed request is never accepted twice
	SignedAt   int64  // first signed mint, unix seconds: unsigned mints for the key are refused from then on

	ownName string // Own's name from the vessel record, refreshed each pass rather than stored
}

type stationNames struct {
	mu      sync.Mutex
	wmu     sync.Mutex // serializes writes to the stations table; see write
	m       map[string]*stationMeta
	dirty   map[string]bool
	store   *store          // nil without the vessel record: names then live only as long as the process
	locked  map[string]bool // LOCKED_STATIONS: no operator or vessel name, and no signed name changes
	changes map[string][]int64
}

func newStationNames() *stationNames {
	n := &stationNames{m: map[string]*stationMeta{}, dirty: map[string]bool{}, locked: map[string]bool{}, changes: map[string][]int64{}}
	for _, s := range strings.Split(os.Getenv("LOCKED_STATIONS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			n.locked[s] = true
		}
	}
	return n
}

func (n *stationNames) meta(id string) *stationMeta {
	m := n.m[id]
	if m == nil {
		m = &stationMeta{}
		n.m[id] = m
	}
	return m
}

// attach loads the stations table and writes later changes there.
func (n *stationNames) attach(s *store) error {
	rows, err := s.db.Query(`SELECT id, near, own, name, vessel_name, signed_ts, signed_at FROM stations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	n.mu.Lock()
	defer n.mu.Unlock()
	for rows.Next() {
		var id string
		m := &stationMeta{}
		if err := rows.Scan(&id, &m.Near, &m.Own, &m.Name, &m.VesselName, &m.SignedTS, &m.SignedAt); err != nil {
			return err
		}
		n.m[id] = m
	}
	n.store = s
	return rows.Err()
}

// flush writes changed stations. A failed write stays dirty for the next pass.
func (n *stationNames) flush() error {
	n.mu.Lock()
	ids := make([]string, 0, len(n.dirty))
	for id := range n.dirty {
		ids = append(ids, id)
	}
	n.mu.Unlock()
	return n.write(ids)
}

// write saves the given stations now. wmu spans the snapshot and the writes, so a pass that read a station
// earlier can never land after one that read it later and roll it back. A station that fails stays dirty.
func (n *stationNames) write(ids []string) error {
	n.wmu.Lock()
	defer n.wmu.Unlock()
	n.mu.Lock()
	if n.store == nil || len(ids) == 0 {
		n.mu.Unlock()
		return nil
	}
	type row struct {
		id string
		m  stationMeta
	}
	var rows []row
	for _, id := range ids {
		if m := n.m[id]; m != nil {
			rows = append(rows, row{id, *m})
			delete(n.dirty, id)
		}
	}
	db := n.store.db
	n.mu.Unlock()
	var errs []error
	for _, r := range rows {
		_, err := db.Exec(`INSERT INTO stations (id, near, own, name, vessel_name, signed_ts, signed_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET near=excluded.near, own=excluded.own, name=excluded.name, vessel_name=excluded.vessel_name,
			signed_ts=excluded.signed_ts, signed_at=excluded.signed_at`,
			r.id, r.m.Near, r.m.Own, r.m.Name, r.m.VesselName, r.m.SignedTS, r.m.SignedAt)
		if err != nil {
			errs = append(errs, err)
			n.mu.Lock()
			n.dirty[r.id] = true
			n.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}

// decorate fills a row's name fields from its station's.
func (n *stationNames) decorate(r *stationRow) {
	id := r.Station
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.m[id]
	if m == nil {
		return
	}
	r.Near, r.MMSI = m.Near, m.Own
	if n.locked[id] {
		return
	}
	switch {
	case m.Name != "":
		r.Name, r.NameFrom = m.Name, "operator"
	case m.VesselName != "":
		r.Name, r.NameFrom = m.VesselName, "vessel"
	case m.ownName != "":
		r.Name, r.NameFrom = m.ownName, "vessel"
	}
}

// decideOwn picks a station's own vessel from the MMSIs it sent as !AIVDO and when it last sent each. Only
// MMSIs with a known country count. With none, the previous decision stands (a moored boat may be silent for
// days); with two inside ownSettle, there is no own vessel until one has had the hour alone.
func decideOwn(cands map[uint32]int64, now time.Time) (mmsi uint32, decided bool) {
	var latest uint32
	var latestT int64
	n := 0
	for m, t := range cands {
		if flagOf(m) == "" {
			continue
		}
		n++
		if t > latestT {
			latest, latestT = m, t
		}
	}
	if n == 0 {
		return 0, false
	}
	settle := now.Add(-ownSettle).Unix()
	for m, t := range cands {
		if m != latest && flagOf(m) != "" && t > settle {
			return 0, true
		}
	}
	return latest, true
}

// runStationNames refreshes own vessels every minute and coverage labels every labelEvery minutes, and
// saves what changed. The first label pass waits a minute, for stations to report after a restart.
func (p *Pipeline) runStationNames() {
	for i := 0; ; i++ {
		now := time.Now()
		p.refreshOwn(now)
		if i%labelEvery == 1 {
			p.refreshLabels(now)
		}
		if err := p.names.flush(); err != nil {
			log.Printf("station names: %v", err)
		}
		time.Sleep(time.Minute)
	}
}

func (p *Pipeline) refreshOwn(now time.Time) {
	owns := p.stations.ownShips()
	type pick struct {
		id   string
		mmsi uint32
	}
	var picks []pick
	n := p.names
	n.mu.Lock()
	for id, cands := range owns {
		mmsi, decided := decideOwn(cands, now)
		m := n.meta(id)
		if decided && m.Own != mmsi {
			m.Own = mmsi
			n.dirty[id] = true
		}
	}
	for id, m := range n.m {
		if m.Own != 0 {
			picks = append(picks, pick{id, m.Own})
		}
	}
	n.mu.Unlock()
	names := map[string]string{}
	for _, pk := range picks { // outside the lock: the vessel record is a SQLite read
		if name, _, err := p.vesselName(pk.mmsi); err == nil {
			names[pk.id] = name
		}
	}
	n.mu.Lock()
	for id, name := range names {
		if m := n.m[id]; m != nil {
			m.ownName = name
		}
	}
	n.mu.Unlock()
}

// refreshLabels labels each volunteer station by the place nearest the median of its finest coverage cells over
// the coverage map's window, its own ship's included as every position it heard is, for those with at least
// labelMinPoints cells.
func (p *Pipeline) refreshLabels(now time.Time) {
	p.vmu.RLock()
	var s stationSeries
	if p.ch != nil {
		s = p.ch.series
	}
	p.vmu.RUnlock()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	points, err := s.stationPoints(ctx, now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -coverageDays))
	if err != nil {
		log.Printf("station labels: %v", err)
		return
	}
	labels := make(map[string]string, len(points))
	for id, pts := range points {
		if len(pts) < labelMinPoints {
			continue
		}
		lats, lons := make([]float64, len(pts)), make([]float64, len(pts))
		for i, pt := range pts {
			lats[i], lons[i] = pt[0], pt[1]
		}
		lat, lon := medianPoint(lats, lons)
		labels[id] = nearLabel(lat, lon)
	}
	n := p.names
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, l := range labels {
		if m := n.meta(id); m.Near != l {
			m.Near = l
			n.dirty[id] = true
		}
	}
}

// ---- names at mint ----

// normalizeName trims a name and collapses its whitespace. It returns "" for a name that breaks the
// character rules: letters, marks, digits, spaces, and a little punctuation, so no control characters,
// bidi overrides, emoji, URLs, or addresses.
func normalizeName(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", nil
	}
	if n := len([]rune(s)); n > nameMaxRunes {
		return "", errors.New("a name is at most " + strconv.Itoa(nameMaxRunes) + " characters")
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" .,'-&()/#", r) {
			return "", errors.New("a name may use letters, digits, spaces, and . , ' - & ( ) / #")
		}
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "www.") {
		return "", errors.New("a name may not be a web address")
	}
	if reservedName(lower) {
		return "", errors.New("that name is reserved")
	}
	return s, nil
}

// reservedName: names that would pass a station off as the project's own, or as an authority. The brand
// words are refused anywhere; the rest only as whole words, so "Badminton Bay" passes.
func reservedName(lower string) bool {
	for _, w := range []string{"open waters", "openwaters", "aiscast"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	words := " " + strings.Join(strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ") + " "
	for _, w := range []string{"official", "admin", "moderator", "uscg", "coast guard", "noaa"} {
		if strings.Contains(words, " "+w+" ") {
			return true
		}
	}
	return false
}

// mintSignature is what a signed mint signs: plain lines, so no JSON canonicalization is needed.
func mintSignature(pubkey string, ts int64, bindIP bool, name, vesselName string) []byte {
	b := "0"
	if bindIP {
		b = "1"
	}
	return []byte(strings.Join([]string{"aiscast-key", pubkey, strconv.FormatInt(ts, 10), b, name, vesselName}, "\n"))
}

// checkMint decides whether a mint for pubkey may go ahead. A signed request must verify, carry a ts within
// mintSkew, and be newer than the last one accepted for the key; it then locks the key to signed mints. An
// unsigned request is refused when it names the station or when the key has signed before.
func (n *stationNames) checkMint(req mintRequest, now time.Time) error {
	id := "station:ed25519:" + req.Pubkey
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.m[id]
	if req.Sig == "" {
		if req.Name != nil || req.VesselName != nil {
			return errors.New("a name needs a signed request")
		}
		if m != nil && m.SignedAt != 0 {
			return errors.New("this key signs its requests: update the client so it signs this one too")
		}
		return nil
	}
	pk, _ := base64.RawURLEncoding.DecodeString(req.Pubkey) // mintPersonal has already checked it
	sig, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil || len(pk) != ed25519.PublicKeySize || !ed25519.Verify(pk, mintSignature(req.Pubkey, req.TS, req.BindIP, deref(req.Name), deref(req.VesselName)), sig) {
		return errors.New("signature does not verify")
	}
	if d := now.Sub(time.Unix(req.TS, 0)); d > mintSkew || d < -mintSkew {
		return errors.New("ts must be within 5 minutes of the server clock")
	}
	if m != nil && req.TS <= m.SignedTS {
		return errors.New("ts must be later than the last signed request")
	}
	m = n.meta(id)
	m.SignedTS = req.TS
	if m.SignedAt == 0 {
		m.SignedAt = now.Unix()
	}
	n.dirty[id] = true
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applyNames stores a signed mint's names on its stations: the token's own, and the UDP station of the
// requester's address when it binds it. A name that breaks a rule is skipped and the reason returned; the
// mint still goes ahead, because a refused name must never stop a station sharing data.
func (n *stationNames) applyNames(ids []string, req mintRequest, now time.Time) string {
	if req.Name == nil && req.VesselName == nil {
		return ""
	}
	name, err := normalizeName(deref(req.Name))
	if err != nil {
		return err.Error()
	}
	vessel, err := normalizeName(deref(req.VesselName))
	if err != nil {
		return err.Error()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, id := range ids {
		if n.locked[id] {
			return "this station's name is locked"
		}
	}
	if req.Name != nil && name != "" {
		mine := map[string]bool{}
		for _, id := range ids {
			mine[id] = true
		}
		for id, m := range n.m { // unique among chosen names, so the top station's name cannot be copied
			if !mine[id] && strings.EqualFold(m.Name, name) {
				return "another station already has that name"
			}
		}
	}
	day := now.Add(-24 * time.Hour).Unix()
	key := ids[0]
	recent := n.changes[key][:0]
	for _, t := range n.changes[key] {
		if t > day {
			recent = append(recent, t)
		}
	}
	if len(recent) >= nameChangesDay {
		n.changes[key] = recent
		return "too many name changes today"
	}
	n.changes[key] = append(recent, now.Unix())
	for _, id := range ids {
		m := n.meta(id)
		if req.Name != nil {
			m.Name = name
		}
		if req.VesselName != nil {
			m.VesselName = vessel
		}
		n.dirty[id] = true
	}
	return ""
}

// stationsSchema is the stations table in the vessel record.
const stationsSchema = `
CREATE TABLE IF NOT EXISTS stations (
	id          TEXT    PRIMARY KEY,           -- base station id: station:ed25519:<key>, udp:<hash>, mmsi:<n>
	near        TEXT    NOT NULL DEFAULT '',   -- coverage label
	own         INTEGER NOT NULL DEFAULT 0,    -- own vessel's MMSI, from its !AIVDO
	name        TEXT    NOT NULL DEFAULT '',   -- chosen by the operator
	vessel_name TEXT    NOT NULL DEFAULT '',   -- sent by the Signal K plugin
	signed_ts   INTEGER NOT NULL DEFAULT 0,    -- ts of the last accepted signed mint
	signed_at   INTEGER NOT NULL DEFAULT 0     -- first signed mint, unix seconds; unsigned mints refused after
);`
