package sharing

import (
	"os"
	"strings"
	"testing"

	"github.com/cozy/cozy-stack/pkg/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/tests/testutils"
	uuid "github.com/satori/go.uuid"
	"github.com/stretchr/testify/assert"
)

var inst *instance.Instance

// Some doctypes for the tests
const testDoctype = "io.cozy.sharing.tests"
const foos = "io.cozy.sharing.test.foos"
const bars = "io.cozy.sharing.test.bars"
const bazs = "io.cozy.sharing.test.bazs"

func TestRevGeneration(t *testing.T) {
	assert.Equal(t, 1, RevGeneration("1-aaa"))
	assert.Equal(t, 3, RevGeneration("3-123"))
	assert.Equal(t, 10, RevGeneration("10-1f2"))
}

func TestComputePossibleAncestors(t *testing.T) {
	wants := []string{"2-b"}
	haves := []string{"1-a", "2-a", "3-a"}
	pas := computePossibleAncestors(wants, haves)
	expected := []string{"1-a"}
	assert.Equal(t, expected, pas)

	wants = []string{"2-b", "2-c", "4-b"}
	haves = []string{"1-a", "2-a", "3-a", "4-a"}
	pas = computePossibleAncestors(wants, haves)
	expected = []string{"1-a", "3-a"}
	assert.Equal(t, expected, pas)

	wants = []string{"5-b"}
	haves = []string{"1-a", "2-a", "3-a"}
	pas = computePossibleAncestors(wants, haves)
	expected = []string{"3-a"}
	assert.Equal(t, expected, pas)
}

func uuidv4() string {
	id, _ := uuid.NewV4()
	return id.String()
}

func createASharedRef(t *testing.T, id string) {
	ref := SharedRef{
		SID:       testDoctype + "/" + uuidv4(),
		Revisions: []string{"1-aaa"},
		Infos: map[string]SharedInfo{
			id: {Rule: 0},
		},
	}
	err := couchdb.CreateNamedDocWithDB(inst, &ref)
	assert.NoError(t, err)
}

func TestSequenceNumber(t *testing.T) {
	s := &Sharing{SID: uuidv4(), Members: []Member{
		{Status: MemberStatusOwner, Name: "Alice"},
		{Status: MemberStatusReady, Name: "Bob"},
	}}
	nb := 5
	for i := 0; i < nb; i++ {
		createASharedRef(t, s.SID)
	}
	m := &s.Members[1]

	rid, err := s.replicationID(m)
	assert.NoError(t, err)
	assert.Equal(t, "sharing-"+s.SID+"-1", rid)

	seq, err := s.getLastSeqNumber(inst, m, "replicator")
	assert.NoError(t, err)
	assert.Empty(t, seq)
	_, _, seq, err = s.callChangesFeed(inst, seq)
	assert.NoError(t, err)
	assert.NotEmpty(t, seq)
	assert.Equal(t, nb+1, RevGeneration(seq)) // +1 because of the view creation
	err = s.UpdateLastSequenceNumber(inst, m, "replicator", seq)
	assert.NoError(t, err)

	seqU, err := s.getLastSeqNumber(inst, m, "upload")
	assert.NoError(t, err)
	assert.Empty(t, seqU)
	err = s.UpdateLastSequenceNumber(inst, m, "upload", "2-abc")
	assert.NoError(t, err)

	seq2, err := s.getLastSeqNumber(inst, m, "replicator")
	assert.NoError(t, err)
	assert.Equal(t, seq, seq2)

	err = s.UpdateLastSequenceNumber(inst, m, "replicator", "2-abc")
	assert.NoError(t, err)
	seq3, err := s.getLastSeqNumber(inst, m, "replicator")
	assert.NoError(t, err)
	assert.Equal(t, seq, seq3)
}

func createDoc(t *testing.T, doctype, id string, attrs map[string]interface{}) *couchdb.JSONDoc {
	attrs["_id"] = id
	doc := couchdb.JSONDoc{
		M:    attrs,
		Type: doctype,
	}
	err := couchdb.CreateNamedDocWithDB(inst, &doc)
	assert.NoError(t, err)
	return &doc
}

func updateDoc(t *testing.T, doctype, id, rev string, attrs map[string]interface{}) *couchdb.JSONDoc {
	doc := couchdb.JSONDoc{
		M:    attrs,
		Type: doctype,
	}
	doc.SetID(id)
	doc.SetRev(rev)
	err := couchdb.UpdateDoc(inst, &doc)
	assert.NoError(t, err)
	return &doc
}

func getSharedRef(t *testing.T, doctype, id string) *SharedRef {
	var ref SharedRef
	err := couchdb.GetDoc(inst, consts.Shared, doctype+"/"+id, &ref)
	assert.NoError(t, err)
	return &ref
}

func assertNbSharedRef(t *testing.T, expected int) {
	nb, err := couchdb.CountAllDocs(inst, consts.Shared)
	assert.NoError(t, err)
	assert.Equal(t, expected, nb)
}

func TestInitialCopy(t *testing.T) {
	// Start with an empty io.cozy.shared database
	couchdb.DeleteDB(inst, consts.Shared)
	couchdb.CreateDB(inst, consts.Shared)

	// Create some documents that are not shared
	for i := 0; i < 10; i++ {
		id := uuidv4()
		createDoc(t, testDoctype, id, map[string]interface{}{"foo": id})
	}

	s := Sharing{SID: uuidv4()}

	// Rule 0 is local => no copy of documents
	settingsDocID := uuidv4()
	createDoc(t, consts.Settings, settingsDocID, map[string]interface{}{"foo": settingsDocID})
	s.Rules = append(s.Rules, Rule{
		Title:   "A local rule",
		DocType: consts.Settings,
		Values:  []string{settingsDocID},
		Local:   true,
	})
	s.InitialCopy(inst, s.Rules[len(s.Rules)-1], len(s.Rules)-1)
	nbShared := 0
	assertNbSharedRef(t, nbShared)

	// Rule 1 is a unique shared document
	oneID := uuidv4()
	oneDoc := createDoc(t, testDoctype, oneID, map[string]interface{}{"foo": "quuuuux"})
	s.Rules = append(s.Rules, Rule{
		Title:   "A unique document",
		DocType: testDoctype,
		Values:  []string{oneID},
	})
	s.InitialCopy(inst, s.Rules[len(s.Rules)-1], len(s.Rules)-1)
	nbShared++
	assertNbSharedRef(t, nbShared)
	oneRef := getSharedRef(t, testDoctype, oneID)
	assert.NotNil(t, oneRef)
	assert.Equal(t, testDoctype+"/"+oneID, oneRef.SID)
	assert.Equal(t, []string{oneDoc.Rev()}, oneRef.Revisions)
	assert.Contains(t, oneRef.Infos, s.SID)
	assert.Equal(t, 1, oneRef.Infos[s.SID].Rule)

	// Rule 2 is with a selector
	twoIDs := []string{uuidv4(), uuidv4(), uuidv4()}
	for _, id := range twoIDs {
		createDoc(t, testDoctype, id, map[string]interface{}{"foo": "bar"})
	}
	s.Rules = append(s.Rules, Rule{
		Title:    "the foo: bar documents",
		DocType:  testDoctype,
		Selector: "foo",
		Values:   []string{"bar"},
	})
	s.InitialCopy(inst, s.Rules[len(s.Rules)-1], len(s.Rules)-1)
	nbShared += len(twoIDs)
	assertNbSharedRef(t, nbShared)
	for _, id := range twoIDs {
		twoRef := getSharedRef(t, testDoctype, id)
		assert.NotNil(t, twoRef)
		assert.Contains(t, twoRef.Infos, s.SID)
		assert.Equal(t, 2, twoRef.Infos[s.SID].Rule)
	}

	// Rule 3 is another rule with a selector
	threeIDs := []string{uuidv4(), uuidv4(), uuidv4()}
	for i, id := range threeIDs {
		u := "u"
		for j := 0; j < i; j++ {
			u += "u"
		}
		createDoc(t, testDoctype, id, map[string]interface{}{"foo": "q" + u + "x"})
	}
	s.Rules = append(s.Rules, Rule{
		Title:    "the foo: baz documents",
		DocType:  testDoctype,
		Selector: "foo",
		Values:   []string{"qux", "quux", "quuux"},
	})
	s.InitialCopy(inst, s.Rules[len(s.Rules)-1], len(s.Rules)-1)
	nbShared += len(threeIDs)
	assertNbSharedRef(t, nbShared)
	for _, id := range threeIDs {
		threeRef := getSharedRef(t, testDoctype, id)
		assert.NotNil(t, threeRef)
		assert.Contains(t, threeRef.Infos, s.SID)
		assert.Equal(t, 3, threeRef.Infos[s.SID].Rule)
	}

	// Another member accepts the sharing
	for r, rule := range s.Rules {
		s.InitialCopy(inst, rule, r)
	}
	assertNbSharedRef(t, nbShared)

	// A document is added
	addID := uuidv4()
	twoIDs = append(twoIDs, addID)
	createDoc(t, testDoctype, addID, map[string]interface{}{"foo": "bar"})

	// A document is updated
	updateID := twoIDs[0]
	updateRef := getSharedRef(t, testDoctype, updateID)
	updateRev := updateRef.Revisions[0]
	updateDoc := updateDoc(t, testDoctype, updateID, updateRev, map[string]interface{}{"foo": "bar", "updated": true})

	// A third member accepts the sharing
	for r, rule := range s.Rules {
		s.InitialCopy(inst, rule, r)
	}
	nbShared++
	assertNbSharedRef(t, nbShared)
	for _, id := range twoIDs {
		twoRef := getSharedRef(t, testDoctype, id)
		assert.NotNil(t, twoRef)
		assert.Contains(t, twoRef.Infos, s.SID)
		assert.Equal(t, 2, twoRef.Infos[s.SID].Rule)
		if id == updateID {
			if assert.Len(t, twoRef.Revisions, 2) {
				assert.Equal(t, updateRev, twoRef.Revisions[0])
				assert.Equal(t, updateDoc.Rev(), twoRef.Revisions[1])
			}
		}
	}

	// Another sharing
	s2 := Sharing{SID: uuidv4()}
	s2.Rules = append(s2.Rules, Rule{
		Title:    "the foo: baz documents",
		DocType:  testDoctype,
		Selector: "foo",
		Values:   []string{"qux", "quux", "quuux"},
	})
	s2.InitialCopy(inst, s2.Rules[len(s2.Rules)-1], len(s2.Rules)-1)
	assertNbSharedRef(t, nbShared)
	for _, id := range threeIDs {
		threeRef := getSharedRef(t, testDoctype, id)
		assert.NotNil(t, threeRef)
		assert.Contains(t, threeRef.Infos, s.SID)
		assert.Equal(t, 3, threeRef.Infos[s.SID].Rule)
		assert.Contains(t, threeRef.Infos, s2.SID)
		assert.Equal(t, 0, threeRef.Infos[s2.SID].Rule)
	}
}

func createSharedRef(t *testing.T, sharingID, sid string, revisions []string) *SharedRef {
	ref := SharedRef{
		SID:       sid,
		Revisions: revisions,
		Infos: map[string]SharedInfo{
			sharingID: {Rule: 0},
		},
	}
	err := couchdb.CreateNamedDocWithDB(inst, &ref)
	assert.NoError(t, err)
	return &ref
}

func appendRevisionToSharedRef(t *testing.T, ref *SharedRef, revision string) {
	ref.Revisions = append(ref.Revisions, revision)
	err := couchdb.UpdateDoc(inst, ref)
	assert.NoError(t, err)
}

func TestCallChangesFeed(t *testing.T) {
	// Start with an empty io.cozy.shared database
	couchdb.DeleteDB(inst, consts.Shared)
	couchdb.CreateDB(inst, consts.Shared)

	foobars := "io.cozy.tests.foobars"
	id1 := uuidv4()
	id2 := uuidv4()
	s := Sharing{
		SID: uuidv4(),
		Rules: []Rule{
			{
				Title:   "foobars rule",
				DocType: foobars,
				Values:  []string{id1, id2},
			},
		},
	}
	ref1 := createSharedRef(t, s.SID, foobars+"/"+id1, []string{"1-aaa"})
	ref2 := createSharedRef(t, s.SID, foobars+"/"+id2, []string{"3-bbb"})
	appendRevisionToSharedRef(t, ref1, "2-ccc")

	changes, indexes, seq, err := s.callChangesFeed(inst, "")
	assert.NoError(t, err)
	assert.NotEmpty(t, seq)
	assert.Equal(t, 3, RevGeneration(seq))
	assert.Equal(t, []string{"2-ccc"}, (*changes)[ref1.SID])
	assert.Equal(t, []string{"3-bbb"}, (*changes)[ref2.SID])
	expected := map[string]int{
		foobars + "/" + id1: 0,
		foobars + "/" + id2: 0,
	}
	assert.Equal(t, expected, indexes)

	changes, _, newSeq, err := s.callChangesFeed(inst, seq)
	assert.NoError(t, err)
	assert.Equal(t, seq, newSeq)
	assert.Empty(t, changes)

	appendRevisionToSharedRef(t, ref1, "3-ddd")
	changes, _, newSeq, err = s.callChangesFeed(inst, seq)
	assert.NoError(t, err)
	assert.NotEmpty(t, seq)
	assert.Equal(t, 4, RevGeneration(newSeq))
	assert.Equal(t, []string{"3-ddd"}, (*changes)[ref1.SID])
	assert.NotContains(t, *changes, ref2.SID)
}

func stripGenerations(revs ...string) []interface{} {
	res := make([]interface{}, len(revs))
	for i, rev := range revs {
		parts := strings.SplitN(rev, "-", 2)
		res[i] = parts[1]
	}
	return res
}

func TestGetMissingDocs(t *testing.T) {
	hellos := "io.cozy.tests.hellos"
	couchdb.CreateDB(inst, hellos)

	id1 := uuidv4()
	doc1 := createDoc(t, hellos, id1, map[string]interface{}{"hello": id1})
	id2 := uuidv4()
	doc2 := createDoc(t, hellos, id2, map[string]interface{}{"hello": id2})
	doc2b := updateDoc(t, hellos, id2, doc2.Rev(), map[string]interface{}{"hello": id2, "bis": true})
	id3 := uuidv4()
	doc3 := createDoc(t, hellos, id3, map[string]interface{}{"hello": id3})
	doc3b := updateDoc(t, hellos, id3, doc3.Rev(), map[string]interface{}{"hello": id3, "bis": true})
	s := Sharing{
		SID: uuidv4(),
		Rules: []Rule{
			{
				Title:   "hellos rule",
				DocType: hellos,
				Values:  []string{id1, id2, id3},
			},
		},
	}

	missings := &Missings{
		hellos + "/" + id1: MissingEntry{
			Missing: []string{doc1.Rev()},
		},
		hellos + "/" + id2: MissingEntry{
			Missing: []string{doc2.Rev(), doc2b.Rev()},
		},
		hellos + "/" + id3: MissingEntry{
			Missing: []string{doc3b.Rev()},
			PAs:     []string{doc3.Rev()},
		},
	}
	results, err := s.getMissingDocs(inst, missings)
	assert.NoError(t, err)
	assert.Contains(t, *results, hellos)
	assert.Len(t, (*results)[hellos], 4)

	var one, two, twob, three map[string]interface{}
	for i, doc := range (*results)[hellos] {
		switch doc["_id"] {
		case id1:
			one = (*results)[hellos][i]
		case id2:
			if _, ok := doc["bis"]; ok {
				twob = (*results)[hellos][i]
			} else {
				two = (*results)[hellos][i]
			}
		case id3:
			three = (*results)[hellos][i]
		}
	}
	assert.NotNil(t, twob)
	assert.NotNil(t, three)

	assert.NotNil(t, one)
	assert.Equal(t, doc1.Rev(), one["_rev"])
	assert.Equal(t, id1, one["hello"])
	assert.Equal(t, float64(1), one["_revisions"].(map[string]interface{})["start"])
	assert.Equal(t, stripGenerations(doc1.Rev()), one["_revisions"].(map[string]interface{})["ids"])

	assert.NotNil(t, two)
	assert.Equal(t, doc2.Rev(), two["_rev"])
	assert.Equal(t, id2, two["hello"])
	assert.Equal(t, float64(1), two["_revisions"].(map[string]interface{})["start"])
	assert.Equal(t, stripGenerations(doc2.Rev()), two["_revisions"].(map[string]interface{})["ids"])

	assert.NotNil(t, twob)
	assert.Equal(t, doc2b.Rev(), twob["_rev"])
	assert.Equal(t, id2, twob["hello"])
	assert.Equal(t, float64(2), twob["_revisions"].(map[string]interface{})["start"])
	assert.Equal(t, stripGenerations(doc2b.Rev(), doc2.Rev()), twob["_revisions"].(map[string]interface{})["ids"])

	assert.NotNil(t, three)
	assert.Equal(t, doc3b.Rev(), three["_rev"])
	assert.Equal(t, id3, three["hello"])
	assert.Equal(t, float64(2), three["_revisions"].(map[string]interface{})["start"])
	assert.Equal(t, stripGenerations(doc3b.Rev(), doc3.Rev()), three["_revisions"].(map[string]interface{})["ids"])
}

func getDoc(t *testing.T, doctype, id string) *couchdb.JSONDoc {
	var doc couchdb.JSONDoc
	err := couchdb.GetDoc(inst, doctype, id, &doc)
	assert.NoError(t, err)
	return &doc
}

func assertNoDoc(t *testing.T, doctype, id string) {
	var doc couchdb.JSONDoc
	err := couchdb.GetDoc(inst, doctype, id, &doc)
	assert.Error(t, err)
}

func TestApplyBulkDocs(t *testing.T) {
	// Start with an empty io.cozy.shared database
	couchdb.DeleteDB(inst, consts.Shared)
	couchdb.CreateDB(inst, consts.Shared)
	couchdb.CreateDB(inst, foos)

	s := Sharing{
		SID: uuidv4(),
		Rules: []Rule{
			{
				Title:    "foos rule",
				DocType:  foos,
				Selector: "hello",
				Values:   []string{"world"},
			},
			{
				Title:    "bars rule",
				DocType:  bars,
				Selector: "hello",
				Values:   []string{"world"},
			},
			{
				Title:    "bazs rule",
				DocType:  bazs,
				Selector: "hello",
				Values:   []string{"world"},
			},
		},
	}
	s2 := Sharing{
		SID: uuidv4(),
		Rules: []Rule{
			{
				Title:    "bars rule",
				DocType:  bars,
				Selector: "hello",
				Values:   []string{"world"},
			},
		},
	}

	// Add a new document
	fooOneID := uuidv4()
	payload := DocsByDoctype{
		foos: DocsList{
			{
				"_id":  fooOneID,
				"_rev": "1-abc",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"abc"},
				},
				"hello":  "world",
				"number": "one",
			},
		},
	}
	err := s.ApplyBulkDocs(inst, payload)
	assert.NoError(t, err)
	nbShared := 1
	assertNbSharedRef(t, nbShared)
	doc := getDoc(t, foos, fooOneID)
	assert.Equal(t, "1-abc", doc.Rev())
	assert.Equal(t, "one", doc.Get("number"))
	ref := getSharedRef(t, foos, fooOneID)
	assert.Equal(t, []string{"1-abc"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 0, ref.Infos[s.SID].Rule)

	// Update a document
	payload = DocsByDoctype{
		foos: DocsList{
			{
				"_id":  fooOneID,
				"_rev": "2-def",
				"_revisions": map[string]interface{}{
					"start": 2,
					"ids":   []string{"def", "abc"},
				},
				"hello":  "world",
				"number": "one bis",
			},
		},
	}
	err = s.ApplyBulkDocs(inst, payload)
	assert.NoError(t, err)
	assertNbSharedRef(t, nbShared)
	doc = getDoc(t, foos, fooOneID)
	assert.Equal(t, "2-def", doc.Rev())
	assert.Equal(t, "one bis", doc.Get("number"))
	ref = getSharedRef(t, foos, fooOneID)
	assert.Equal(t, []string{"1-abc", "2-def"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 0, ref.Infos[s.SID].Rule)

	// Create a reference for another sharing, on a database that does not exist
	barZeroID := uuidv4()
	payload = DocsByDoctype{
		bars: DocsList{
			{
				"_id":  barZeroID,
				"_rev": "1-111",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"111"},
				},
				"hello":  "world",
				"number": "zero",
			},
		},
	}
	err = s2.ApplyBulkDocs(inst, payload)
	assert.NoError(t, err)
	nbShared++
	assertNbSharedRef(t, nbShared)
	doc = getDoc(t, bars, barZeroID)
	assert.Equal(t, "1-111", doc.Rev())
	assert.Equal(t, "zero", doc.Get("number"))
	ref = getSharedRef(t, bars, barZeroID)
	assert.Equal(t, []string{"1-111"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s2.SID)
	assert.Equal(t, 0, ref.Infos[s2.SID].Rule)

	// Add documents for two doctypes at the same time
	barTwoID := uuidv4()
	bazThreeID := uuidv4()
	bazFourID := uuidv4()
	payload = DocsByDoctype{
		bars: DocsList{
			{
				"_id":  barTwoID,
				"_rev": "2-caa",
				"_revisions": map[string]interface{}{
					"start": 2,
					"ids":   []string{"caa", "baa"},
				},
				"hello":  "world",
				"number": "two",
			},
		},
		bazs: DocsList{
			{
				"_id":  bazThreeID,
				"_rev": "1-ddd",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"ddd"},
				},
				"hello":  "world",
				"number": "three",
			},
			{
				"_id":  bazFourID,
				"_rev": "1-eee",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"eee"},
				},
				"hello":  "world",
				"number": "four",
			},
		},
	}
	err = s.ApplyBulkDocs(inst, payload)
	assert.NoError(t, err)
	nbShared += 3
	assertNbSharedRef(t, nbShared)
	doc = getDoc(t, bars, barTwoID)
	assert.Equal(t, "2-caa", doc.Rev())
	assert.Equal(t, "two", doc.Get("number"))
	ref = getSharedRef(t, bars, barTwoID)
	assert.Equal(t, []string{"2-caa"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 1, ref.Infos[s.SID].Rule)
	doc = getDoc(t, bazs, bazThreeID)
	assert.Equal(t, "1-ddd", doc.Rev())
	assert.Equal(t, "three", doc.Get("number"))
	ref = getSharedRef(t, bazs, bazThreeID)
	assert.Equal(t, []string{"1-ddd"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 2, ref.Infos[s.SID].Rule)
	doc = getDoc(t, bazs, bazFourID)
	assert.Equal(t, "1-eee", doc.Rev())
	assert.Equal(t, "four", doc.Get("number"))
	ref = getSharedRef(t, bazs, bazFourID)
	assert.Equal(t, []string{"1-eee"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 2, ref.Infos[s.SID].Rule)

	// And a mix of all cases
	fooFiveID := uuidv4()
	barSixID := uuidv4()
	barSevenID := uuidv4()
	barEightID := uuidv4()
	barEightRev := createDoc(t, bars, barEightID, map[string]interface{}{"hello": "world", "number": "8"}).Rev()
	payload = DocsByDoctype{
		foos: DocsList{
			{
				"_id":  fooOneID,
				"_rev": "3-fab",
				"_revisions": map[string]interface{}{
					"start": 3,
					"ids":   []string{"fab", "def", "abc"},
				},
				"hello":  "world",
				"number": "one ter",
			},
			{
				"_id":  fooFiveID,
				"_rev": "1-aab",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"aab"},
				},
				"hello":  "world",
				"number": "five",
			},
		},
		bars: DocsList{
			{
				"_id":  barSixID,
				"_rev": "1-aac",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"aac"},
				},
				"hello":  "world",
				"number": "six",
			},
			{
				"_id":  barSevenID,
				"_rev": "1-bad",
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{"bad"},
				},
				"not":    "shared",
				"number": "seven",
			},
			{
				"_id":  barEightID,
				"_rev": barEightRev,
				"_revisions": map[string]interface{}{
					"start": 1,
					"ids":   []string{strings.Replace(barEightRev, "1-", "", 1)},
				},
				"hello":  "world",
				"number": "8 bis",
			},
			{
				"_id":  barZeroID,
				"_rev": "2-222",
				"_revisions": map[string]interface{}{
					"start": 2,
					"ids":   []string{"222", "111"},
				},
				"hello":  "world",
				"number": "zero bis",
			},
			{
				"_id":  barTwoID,
				"_rev": "3-daa",
				"_revisions": map[string]interface{}{
					"start": 3,
					"ids":   []string{"daa", "caa", "baa"},
				},
				"hello":  "world",
				"number": "two bis",
			},
		},
		bazs: DocsList{
			{
				"_id":  bazThreeID,
				"_rev": "3-ddf",
				"_revisions": map[string]interface{}{
					"start": 3,
					"ids":   []string{"ddf", "dde", "ddd"},
				},
				"hello":  "world",
				"number": "three bis",
			},
		},
	}
	err = s.ApplyBulkDocs(inst, payload)
	assert.NoError(t, err)
	nbShared += 2 // fooFiveID and barSixID
	assertNbSharedRef(t, nbShared)
	doc = getDoc(t, foos, fooOneID)
	assert.Equal(t, "3-fab", doc.Rev())
	assert.Equal(t, "one ter", doc.Get("number"))
	ref = getSharedRef(t, foos, fooOneID)
	assert.Equal(t, []string{"1-abc", "2-def", "3-fab"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 0, ref.Infos[s.SID].Rule)
	doc = getDoc(t, foos, fooFiveID)
	assert.Equal(t, "1-aab", doc.Rev())
	assert.Equal(t, "five", doc.Get("number"))
	ref = getSharedRef(t, foos, fooFiveID)
	assert.Equal(t, []string{"1-aab"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 0, ref.Infos[s.SID].Rule)
	doc = getDoc(t, bazs, bazThreeID)
	assert.Equal(t, "3-ddf", doc.Rev())
	assert.Equal(t, "three bis", doc.Get("number"))
	ref = getSharedRef(t, bazs, bazThreeID)
	assert.Equal(t, []string{"1-ddd", "3-ddf"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 2, ref.Infos[s.SID].Rule)
	doc = getDoc(t, bars, barSixID)
	assert.Equal(t, "1-aac", doc.Rev())
	assert.Equal(t, "six", doc.Get("number"))
	ref = getSharedRef(t, bars, barSixID)
	assert.Equal(t, []string{"1-aac"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 1, ref.Infos[s.SID].Rule)
	doc = getDoc(t, bars, barTwoID)
	assert.Equal(t, "3-daa", doc.Rev())
	assert.Equal(t, "two bis", doc.Get("number"))
	ref = getSharedRef(t, bars, barTwoID)
	assert.Equal(t, []string{"2-caa", "3-daa"}, ref.Revisions)
	assert.Contains(t, ref.Infos, s.SID)
	assert.Equal(t, 1, ref.Infos[s.SID].Rule)
	// New document rejected because it doesn't match the rules
	assertNoDoc(t, bars, barSevenID)
	// Existing document with no shared reference
	doc = getDoc(t, bars, barEightID)
	assert.Equal(t, barEightRev, doc.Rev())
	assert.Equal(t, "8", doc.Get("number"))
	// Existing document with a shared reference, but not for the good sharing
	doc = getDoc(t, bars, barZeroID)
	assert.Equal(t, "1-111", doc.Rev())
	assert.Equal(t, "zero", doc.Get("number"))
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	testutils.NeedCouchdb()
	setup := testutils.NewSetup(m, "sharing_test_repl")
	inst = setup.GetTestInstance()
	os.Exit(setup.Run())
}
