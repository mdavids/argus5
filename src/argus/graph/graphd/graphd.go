// Copyright (c) 2017
// Author: Jeff Weisberg <jaw @ tcp4me.com>
// Created: 2017-Oct-01 11:12 (EDT)
// Function: graphing

package graphd

import (
	"encoding/binary"
	"math"
	"os"
	"sync"
	"time"

	"argus/argus"
	"argus/clock"
	"argus/config"
	"argus/diag"
)

type graphData struct {
	f         *os.File
	h         *Header
	sampStart int64
	hourStart int64
	dayStart  int64
}

// similar to, but not exactly the same as, argus3.x
const (
	MAGIC     = "AGD5"
	HdrSize   = 1024
	SampSize  = 20
	SummySize = 32
	SampNMax  = 1024
	HourNMax  = 1024
	DayNMax   = 1024
)

type HeaderSect struct {
	Idx    int32
	NMax   int32
	Count  int32
	NSamp  int32
	Min    float32
	Max    float32
	Sigm   float32
	Sigm2  float32
	Exp    float32
	Delt   float32
	Status int32
	Pad    [84]byte // total size = 128
}

type Header struct {
	Magic [4]byte
	Lastt uint32    // times are unix>>2
	Pad   [120]byte // next section aligned @128
	Samp  HeaderSect
	Hour  HeaderSect
	Day   HeaderSect
}

type SampleData struct {
	When   uint32
	Status int32
	Value  float32
	Exp    float32
	Delt   float32
	// total size = 20
}
type SummyData struct {
	When   uint32
	Status int32
	Min    float32
	Max    float32
	Ave    float32
	Stdev  float32
	Exp    float32
	Delt   float32
	// total size = 32
}
type Export struct {
	When   int64
	Status argus.Status
	Value  float32
	Min    float32
	Max    float32
	Stdev  float32
	Exp    float32
	Delt   float32
}

const NLOCK = 251 // prime

var datadir = ""
var dl = diag.Logger("graphd")

var locks = make([]sync.RWMutex, NLOCK)

func init() {

	// verify proper padding
	h := Header{}

	if binary.Size(h.Samp) != 128 {
		dl.Fatal("headerSection botched (%d)", binary.Size(h.Samp))
	}
	if binary.Size(h) != 512 {
		dl.Fatal("header size botched (%d)", binary.Size(h))
	}

}

func Init() {
	cf := config.Cf()
	datadir = cf.Datadir
	// RSN - other graphing params, defaults, ...
}

func Add(file string, when int64, status argus.Status, val float64, yn float64, dn float64) {

	dl.Debug("add graph")
	if datadir == "" {
		dl.Debug("no datadir")
		return
	}

	if status == argus.DEPENDS || status == argus.UNKNOWN {
		status = argus.CLEAR
	}

	lno := lockno(file)
	locks[lno].Lock()
	defer locks[lno].Unlock()

	// find or create file

	file = filename(file)
	g := open(file)
	if g == nil {
		dl.Debug("create %s", file)
		g = create(file)
	}
	if g == nil {
		dl.Debug("cannot open")
		return
	}

	defer g.close()
	g.add(when, status, val, yn, dn)
	g.save()
}

func (g *graphData) add(when int64, status argus.Status, val float64, yn float64, dn float64) {

	dl.Debug("add sample")
	g.seek(g.sampStart + SampSize*int64(g.h.Samp.Idx))
	binary.Write(g.f, binary.BigEndian, &SampleData{
		When:   fromSeconds(when),
		Status: int32(status),
		Value:  float32(val),
		Exp:    float32(yn),
		Delt:   float32(dn),
	})
	g.h.Samp.Idx = (g.h.Samp.Idx + 1) % g.h.Samp.NMax
	g.h.Samp.Count++
	if g.h.Samp.Count > g.h.Samp.NMax {
		g.h.Samp.Count = g.h.Samp.NMax
	}

	// roll?
	lt := time.Unix(toSeconds(g.h.Lastt), 0).Local()
	ct := time.Unix(when, 0).Local()

	if lt.Day() != ct.Day() {
		g.rollDay(float32(val))
	}
	if lt.Hour() != ct.Hour() {
		g.rollHour(float32(val))
	}

	// update header summaries
	g.h.Hour.add(status, float32(val), float32(yn), float32(dn))
	g.h.Day.add(status, float32(val), float32(yn), float32(dn))
	g.h.Lastt = fromSeconds(when)
}

// ################################################################

func (hs *HeaderSect) add(status argus.Status, val, exp, delt float32) {

	hs.NSamp++
	if val < hs.Min {
		hs.Min = val
	}
	if val > hs.Max {
		hs.Max = val
	}

	hs.Sigm += val
	hs.Sigm2 += val * val
	hs.Exp += exp
	hs.Delt += delt

	if int32(status) > hs.Status {
		hs.Status = int32(status)
	}
}

func (g *graphData) rollHour(val float32) {

	g.roll(&g.h.Hour, g.hourStart, val)
}

func (g *graphData) rollDay(val float32) {

	g.roll(&g.h.Day, g.dayStart, val)
}

func (g *graphData) roll(h *HeaderSect, start int64, val float32) {

	if h.NSamp == 0 {
		return
	}

	dl.Debug("roll")
	sum := h.summarize(g.h.Lastt)
	g.seek(start + SummySize*int64(h.Idx))
	binary.Write(g.f, binary.BigEndian, sum)
	h.Idx = (h.Idx + 1) % h.NMax
	h.Count++
	if h.Count > h.NMax {
		h.Count = h.NMax
	}
	h.reset(val)
}

func (hs *HeaderSect) summarize(lastt uint32) *SummyData {

	n := float32(hs.NSamp)
	ave := hs.Sigm / n
	std := hs.Sigm2/n - ave*ave
	if std > 0 {
		std = float32(math.Sqrt(float64(std)))
	} else {
		std = 0
	}

	s := &SummyData{
		When:   lastt,
		Status: hs.Status,
		Min:    hs.Min,
		Max:    hs.Max,
		Ave:    ave,
		Stdev:  std,
		Exp:    hs.Exp / n,
		Delt:   hs.Delt / n,
	}

	return s
}

func (hs *HeaderSect) reset(val float32) {
	hs.NSamp = 0
	hs.Min = val
	hs.Max = val
	hs.Sigm = 0
	hs.Sigm2 = 0
	hs.Exp = 0
	hs.Delt = 0
	hs.Status = 0
}

// ################################################################

func Get(file string, which string, since int64) []*Export {

	if datadir == "" {
		dl.Debug("no datadir")
		return nil
	}
	file = filename(file)

	lno := lockno(file)
	locks[lno].RLock()
	defer locks[lno].RUnlock()

	// open
	g := open(file)
	if g == nil {
		return nil
	}
	defer g.close()

	switch which {
	case "samples":
		return g.getSamples(since)
	case "hours":
		return g.getHourSummy(since)
	case "days":
		return g.getDaySummy(since)
	}
	return nil
}

func (g *graphData) getSamples(since int64) []*Export {

	startRec := int(g.h.Samp.Idx)
	if g.h.Samp.Count < g.h.Samp.NMax {
		startRec = 0
	}
	numRec := int(g.h.Samp.Count)

	// estimate start pos from since
	if since > 0 {
		minago := int((clock.Unix() - since + 59) / 60)
		startRec += int(g.h.Samp.NMax) - minago
		numRec = minago
	}

	r := NewCbufReader(g.f, g.sampStart, int64(g.h.Samp.NMax*SampSize))
	r.Seek(int64(startRec * SampSize))

	var res []*Export
	for i := 0; i < numRec; i++ {
		s := &SampleData{}
		binary.Read(r, binary.BigEndian, s)

		e := &Export{
			When:   toSeconds(s.When),
			Status: argus.Status(s.Status),
			Value:  s.Value,
			Exp:    s.Exp,
			Delt:   s.Delt,
		}
		res = append(res, e)
	}

	return res
}

func (g *graphData) getHourSummy(since int64) []*Export {
	return g.getSummy(&g.h.Hour, g.hourStart, since, 3600)
}
func (g *graphData) getDaySummy(since int64) []*Export {
	return g.getSummy(&g.h.Day, g.dayStart, since, 24*3600)
}

func (g *graphData) getSummy(hs *HeaderSect, start int64, since int64, spu int) []*Export {

	startRec := int(hs.Idx)
	if hs.Count < hs.NMax {
		startRec = 0
	}
	numRec := int(hs.Count)

	// estimate start pos from since
	if since > 0 {
		uago := (int(clock.Unix()-since) + spu - 1) / spu
		startRec += int(hs.NMax) - uago
		numRec = uago
	}

	r := NewCbufReader(g.f, start, int64(hs.NMax*SummySize))
	r.Seek(int64(startRec * SummySize))

	var res []*Export
	for i := 0; i < numRec; i++ {
		s := &SummyData{}
		binary.Read(r, binary.BigEndian, s)

		e := &Export{
			When:   toSeconds(s.When),
			Status: argus.Status(s.Status),
			Value:  s.Ave,
			Stdev:  s.Stdev,
			Min:    s.Min,
			Max:    s.Max,
			Exp:    s.Exp,
			Delt:   s.Delt,
		}
		res = append(res, e)
	}

	return res
}

// ################################################################

func filename(file string) string {
	return datadir + "/gdata/" + file
}

func open(file string) *graphData {

	// open, read header
	f, err := os.OpenFile(file, os.O_RDWR, 0666)
	if err != nil {
		dl.Debug("open failed: %v", err)
		return nil
	}
	g := &graphData{f: f}
	ok := g.readHeader()
	if !ok {
		dl.Verbose("corrupt graph data: %s", filename(file))
		return nil
	}
	return g
}

func create(file string) *graphData {

	f, err := os.Create(file)
	if err != nil {
		dl.Verbose("cannot save graph data: %v", err)
		return nil
	}

	g := &graphData{f: f}
	g.newHeader()
	return g
}

func (g *graphData) seek(pos int64) {
	g.f.Seek(pos, 0)
}

func (g *graphData) readHeader() bool {

	g.h = &Header{}
	g.seek(0)
	binary.Read(g.f, binary.BigEndian, g.h)

	if string(g.h.Magic[:]) != MAGIC {
		return false
	}
	g.initHeader()
	return true
}

func (g *graphData) newHeader() {

	h := &Header{
		Samp: HeaderSect{NMax: SampNMax},
		Hour: HeaderSect{NMax: HourNMax},
		Day:  HeaderSect{NMax: DayNMax},
	}
	copy(h.Magic[:], MAGIC)
	g.h = h
	g.initHeader()
}

func (g *graphData) initHeader() {
	g.sampStart = HdrSize
	g.hourStart = g.sampStart + SampSize*int64(g.h.Samp.NMax)
	g.dayStart = g.hourStart + SummySize*int64(g.h.Hour.NMax)
}

func (g *graphData) save() {
	g.seek(0)
	binary.Write(g.f, binary.BigEndian, g.h)
}

func (g *graphData) close() {
	g.f.Close()
}

func toSeconds(t uint32) int64 {
	return int64(t) << 2
}
func fromSeconds(t int64) uint32 {
	return uint32(t >> 2)
}
func lockno(file string) int {
	return argus.HashDjb2(file) % NLOCK
}