package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// view is which screen a frame shows, read off the text an 80x24 terminal gets.
type view int

const (
	viewUnknown view = iota
	viewHome
	viewCreate
	viewJoin
	viewLobby
	viewLobbyLeave
	viewGame
	viewForfeit
	viewGameOver
)

// gameKeys opens the table's key line on and off turn alike, and under the suit
// picker. "d: draw" is offered only on turn, and a waiting seat read as no screen at
// all never settled its move; the picker drops it too, since d does nothing there.
const gameKeys = "<-/h: left | ->/l: right"

// classify names the screen. The order matters: the footer of every menu repeats
// the home screen's keys, so home is whatever is left.
func classify(lines []string) view {
	text := strings.Join(lines, "\n")
	for _, c := range []struct {
		marker string
		v      view
	}{
		{"Game Over!", viewGameOver},
		{"Leave and forfeit this game?", viewForfeit},
		{gameKeys, viewGame},
		{"want to leave the lobby?", viewLobbyLeave},
		{"Lobby Code:", viewLobby},
		{"[ Create Lobby ]", viewCreate},
		{"private lobby code", viewJoin},
		{"p - Profile", viewHome},
	} {
		if strings.Contains(text, c.marker) {
			return c.v
		}
	}
	return viewUnknown
}

// card is one card as the hand strip prints it: a rank label and a suit glyph.
type card struct {
	rank string
	suit string
}

// crazyTable is what a Crazy Eights frame says the bot needs to choose a move.
type crazyTable struct {
	myTurn bool
	picker bool
	hand   []card
	cursor int // the strip's ">" marker, -1 when none
	top    string
	suit   string
}

var (
	stripCell   = regexp.MustCompile(`([ >*])(10| [2-9AJQK])([♠♥♦♣])`)
	discardFace = regexp.MustCompile(`│(10|[2-9AJQK]) {7,8}│`)
	suitLine    = regexp.MustCompile(`Current Suit: \S+ (Spades|Hearts|Diamonds|Clubs)`)
	lobbyCode   = regexp.MustCompile(`Lobby Code: ([A-Z0-9]{8})`)
	hiddenSeats = regexp.MustCompile(`\.\.\. and (\d+) more`)
	maxPlayers  = regexp.MustCompile(`Max Players: < (\d+) >`)
)

var suitGlyph = map[string]string{"Spades": "♠", "Hearts": "♥", "Diamonds": "♦", "Clubs": "♣"}

// parseCrazy reads a Crazy Eights table. The hand is the strip an 80x24 terminal
// gets - too short for card art - between the turn status and the key hints.
func parseCrazy(lines []string) crazyTable {
	t := crazyTable{cursor: -1}
	status := -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "> YOUR TURN <"):
			t.myTurn, status = true, i
		case strings.Contains(line, "Current turn:"):
			status = i
		case strings.Contains(line, "Pick a suit:"):
			t.picker = true
		case suitLine.MatchString(line):
			t.suit, t.top = suitGlyph[suitLine.FindStringSubmatch(line)[1]], discardAbove(lines[:i])
		case status >= 0 && strings.Contains(line, gameKeys):
			return t
		case status >= 0:
			cards, cursor := stripCards(line)
			if cursor >= 0 {
				t.cursor = len(t.hand) + cursor
			}
			t.hand = append(t.hand, cards...)
		}
	}
	return t
}

// stripCards is the cards on one row of the hand strip and which of them carries the
// cursor, -1 for none.
func stripCards(line string) (cards []card, cursor int) {
	cursor = -1
	for _, m := range stripCell.FindAllStringSubmatch(line, -1) {
		if m[1] == ">" {
			cursor = len(cards)
		}
		cards = append(cards, card{rank: strings.TrimSpace(m[2]), suit: m[3]})
	}
	return cards, cursor
}

// discardAbove is the rank on the face of the nearest card drawn above the suit line,
// which is the discard pile.
func discardAbove(lines []string) string {
	for _, line := range slices.Backward(lines) {
		if m := discardFace.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// move is the keys that play a card or draw: select, then enter or d.
type move struct {
	keys string
	wild bool // an eight: the suit picker opens before the play is sent
}

// choose picks a legal Crazy Eights move: a card matching the suit in play or the
// discard's rank, an eight only when nothing else goes, else a draw. A frame the bot
// cannot read is a draw too, which is always legal - so no turn is left to the clock.
func (t crazyTable) choose() move {
	if t.top == "" || t.suit == "" || len(t.hand) == 0 {
		return move{keys: "d"}
	}
	eight := -1
	for i, c := range t.hand {
		switch {
		case c.rank == "8":
			if eight < 0 {
				eight = i
			}
		case c.suit == t.suit || c.rank == t.top:
			return move{keys: t.selectKeys(i) + "\r"}
		}
	}
	if eight >= 0 {
		return move{keys: t.selectKeys(eight) + "\r", wild: true}
	}
	return move{keys: "d"}
}

// selectKeys moves the cursor to card i: a digit for the first ten, arrows past them.
func (t crazyTable) selectKeys(i int) string {
	if i < 10 {
		return strconv.Itoa(i)
	}
	from := max(t.cursor, 0)
	if i >= from {
		return strings.Repeat(keyRight, i-from)
	}
	return strings.Repeat(keyLeft, from-i)
}

const (
	keyUp    = "\x1b[A"
	keyDown  = "\x1b[B"
	keyRight = "\x1b[C"
	keyLeft  = "\x1b[D"
	keyEnter = "\r"
	keyEsc   = "\x1b"
)

// parseLobbyCode is the code guests join by, or "" before the lobby screen shows it.
func parseLobbyCode(lines []string) string {
	for _, line := range lines {
		if m := lobbyCode.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// guestCount is how many guests the lobby roster lists, hidden rows included.
func guestCount(lines []string) int {
	n := 0
	for _, line := range lines {
		n += strings.Count(line, "[Guest]")
		if m := hiddenSeats.FindStringSubmatch(line); m != nil {
			hidden, _ := strconv.Atoi(m[1])
			n += hidden
		}
	}
	return n
}

// createRows are the create form's rows in cursor order, as they start the line.
var createRows = []string{"Game:", "Max Players:", "Visibility:", "Mode:", "[ Create Lobby ]"}

// createKey is the next key that moves the create form towards a public, casual
// Crazy Eights table for seats players, and enter once it is there. The form is
// read afresh every time, so a key the server has not applied yet is not assumed.
func createKey(lines []string, seats int) string {
	cursor, want := -1, len(createRows)-1
	for _, line := range lines {
		for i, row := range createRows {
			if !strings.Contains(line, row) {
				continue
			}
			if strings.Contains(line, "> "+row) {
				cursor = i
			}
			if i < want && !createRowDone(i, line, seats) {
				want = i
			}
		}
	}
	switch {
	case cursor < 0:
		return ""
	case cursor < want:
		return keyDown
	case cursor > want:
		return keyUp
	case want == len(createRows)-1:
		return keyEnter
	case want == 1 && playersOver(lines, seats):
		return keyLeft
	}
	return keyRight
}

func createRowDone(row int, line string, seats int) bool {
	switch row {
	case 0:
		return strings.Contains(line, "< Crazy Eights >")
	case 1:
		m := maxPlayers.FindStringSubmatch(line)
		return len(m) > 1 && m[1] == strconv.Itoa(seats)
	case 2:
		return strings.Contains(line, "Public")
	case 3:
		return strings.Contains(line, "Casual")
	}
	return true
}

func playersOver(lines []string, seats int) bool {
	for _, line := range lines {
		if m := maxPlayers.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n > seats
		}
	}
	return false
}
