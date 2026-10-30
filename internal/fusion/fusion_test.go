package fusion

import (
	"math"
	"testing"

	"goalclip/internal/config"
	"goalclip/internal/signals"
)

func newEngine(scoreMode bool) (*Engine, *[]Goal) {
	var goals []Goal
	c := config.Default()
	e := New(c.Fusion, c.Clip, scoreMode, Hooks{Goal: func(g Goal) { goals = append(goals, g) }})
	return e, &goals
}

func audioSpike(t, conf float64) signals.Signal {
	return signals.Signal{Type: signals.AudioSpike, Time: t, Confidence: conf}
}

func scoreUp(t float64, ph, pa, h, a int) signals.Signal {
	return signals.Signal{Type: signals.ScoreUp, Time: t, Confidence: 1, PrevHome: ph, PrevAway: pa, Home: h, Away: a}
}

func TestScoreChangeWithAudioIsConfirmedAfterDelay(t *testing.T) {
	e, goals := newEngine(true)
	e.Add(audioSpike(40, 0.9))
	e.Add(scoreUp(46, 0, 0, 1, 0))

	e.Advance(50)
	if len(*goals) != 0 {
		t.Fatal("goal must wait for the confirm delay")
	}
	e.Advance(61)
	if len(*goals) != 1 {
		t.Fatalf("want 1 goal, got %d", len(*goals))
	}
	g := (*goals)[0]
	if g.EventTime != 40 {
		t.Errorf("event time %.1f, want 40 (the audio onset)", g.EventTime)
	}
	if g.Home != 1 || g.Away != 0 {
		t.Errorf("score %d-%d", g.Home, g.Away)
	}
	if g.ClipStart != 20 || g.ClipEnd != 60 {
		t.Errorf("clip %.0f-%.0f, want 20-60", g.ClipStart, g.ClipEnd)
	}
	if g.Confidence < 0.9 {
		t.Errorf("confidence %.2f should be high with audio support", g.Confidence)
	}
}

func TestScoreChangeWithoutAudioUsesScoreLag(t *testing.T) {
	e, goals := newEngine(true)
	e.Add(scoreUp(100, 1, 0, 1, 1))
	e.Advance(120)
	if len(*goals) != 1 {
		t.Fatalf("want 1 goal, got %d", len(*goals))
	}
	g := (*goals)[0]
	if math.Abs(g.EventTime-92) > 0.01 {
		t.Errorf("event time %.1f, want 92 (score time minus 8s lag)", g.EventTime)
	}
	if g.Confidence != 0.7 {
		t.Errorf("confidence %.2f, want 0.7", g.Confidence)
	}
}

func TestDisallowedGoalIsCancelled(t *testing.T) {
	e, goals := newEngine(true)
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Add(signals.Signal{Type: signals.ScoreDown, Time: 108, PrevHome: 1, PrevAway: 0, Home: 0, Away: 0})
	e.Advance(200)
	if len(*goals) != 0 {
		t.Fatalf("a reversed score must not produce a clip, got %+v", *goals)
	}
}

func TestAudioSpikeAloneIgnoredWhenScoreboardIsAvailable(t *testing.T) {
	e, goals := newEngine(true)
	e.Add(audioSpike(40, 1))
	e.Advance(500)
	if len(*goals) != 0 {
		t.Fatal("near-misses must not become goals when the scoreboard is the authority")
	}
}

func TestAudioOnlyModeWithCooldown(t *testing.T) {
	e, goals := newEngine(false)
	e.Add(audioSpike(100, 0.9))
	e.Advance(101)
	e.Add(audioSpike(130, 0.9))
	e.Advance(131)
	e.Add(audioSpike(200, 0.9))
	e.Advance(201)
	if len(*goals) != 2 {
		t.Fatalf("want 2 goals (one suppressed by cooldown), got %d", len(*goals))
	}
	if (*goals)[0].Home != -1 {
		t.Error("audio-only goals have an unknown score")
	}
}

func TestLowConfidenceAudioIgnoredInAudioOnlyMode(t *testing.T) {
	e, goals := newEngine(false)
	e.Add(audioSpike(100, 0.3))
	e.Advance(200)
	if len(*goals) != 0 {
		t.Fatal("weak spike should be ignored")
	}
}

func TestScoreAfterGapIsFlagged(t *testing.T) {
	e, goals := newEngine(true)
	s := scoreUp(300, 0, 0, 1, 0)
	s.AfterGap = true
	e.Add(s)
	e.Advance(320)
	if len(*goals) != 1 || !(*goals)[0].DuringGap {
		t.Fatalf("want one goal flagged DuringGap, got %+v", *goals)
	}
	g := (*goals)[0]
	if g.ClipStart != 298 {
		t.Errorf("clip should start just before the resume point, got %.0f", g.ClipStart)
	}
}

func TestFlushConfirmsPendingGoal(t *testing.T) {
	e, goals := newEngine(true)
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Flush(103)
	if len(*goals) != 1 {
		t.Fatalf("flush should confirm the pending goal, got %d", len(*goals))
	}
}

func TestTwoGoalsCloseTogether(t *testing.T) {
	e, goals := newEngine(true)
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Add(scoreUp(130, 1, 0, 1, 1))
	e.Advance(200)
	if len(*goals) != 2 {
		t.Fatalf("both goals should be kept, got %d", len(*goals))
	}
}

func retractEngine() (*Engine, *[]Goal, *[]Goal) {
	var goals, retracted []Goal
	c := config.Default()
	e := New(c.Fusion, c.Clip, true, Hooks{
		Goal:    func(g Goal) { goals = append(goals, g) },
		Retract: func(g Goal, _ signals.Signal) { retracted = append(retracted, g) },
	})
	return e, &goals, &retracted
}

func scoreDown(t float64, ph, pa, h, a int) signals.Signal {
	return signals.Signal{Type: signals.ScoreDown, Time: t, Confidence: 1, PrevHome: ph, PrevAway: pa, Home: h, Away: a}
}

func TestLateReversalRetractsAConfirmedGoal(t *testing.T) {
	e, goals, retracted := retractEngine()
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Advance(120)
	if len(*goals) != 1 {
		t.Fatalf("goal should be confirmed first, got %d", len(*goals))
	}
	e.Add(scoreDown(190, 1, 0, 0, 0))
	if len(*retracted) != 1 || (*retracted)[0].ID != (*goals)[0].ID {
		t.Fatalf("want the confirmed goal retracted, got %+v", *retracted)
	}
}

func TestReversalOfADifferentScoreDoesNotRetract(t *testing.T) {
	e, _, retracted := retractEngine()
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Advance(120)
	e.Add(scoreDown(190, 3, 2, 2, 2))
	if len(*retracted) != 0 {
		t.Fatalf("unrelated reversal must not retract: %+v", *retracted)
	}
}

func TestReversalAfterTheWindowIsIgnored(t *testing.T) {
	e, _, retracted := retractEngine()
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Advance(120)
	e.Add(scoreDown(900, 1, 0, 0, 0))
	if len(*retracted) != 0 {
		t.Fatalf("a reversal 13 minutes later is not a VAR decision: %+v", *retracted)
	}
}

func TestOnlyTheReversedGoalIsRetracted(t *testing.T) {
	e, goals, retracted := retractEngine()
	e.Add(scoreUp(100, 0, 0, 1, 0))
	e.Add(scoreUp(400, 1, 0, 2, 0))
	e.Advance(450)
	if len(*goals) != 2 {
		t.Fatalf("want 2 goals, got %d", len(*goals))
	}
	e.Add(scoreDown(500, 2, 0, 1, 0))
	if len(*retracted) != 1 || (*retracted)[0].Home != 2 {
		t.Fatalf("only the 2-0 goal should be retracted, got %+v", *retracted)
	}
}

func reviewEngine(active bool) (*Engine, *[]Goal) {
	var goals []Goal
	c := config.Default()
	e := New(c.Fusion, c.Clip, true, Hooks{Goal: func(g Goal) { goals = append(goals, g) }})
	e.SetReviewActive(active)
	return e, &goals
}

func TestRoarWithoutAScoreChangeMakesAReviewClip(t *testing.T) {
	e, goals := reviewEngine(true)
	e.Add(audioSpike(100, 0.9))
	e.Advance(145)
	if len(*goals) != 1 || !(*goals)[0].Review {
		t.Fatalf("want one review clip, got %+v", *goals)
	}
	if (*goals)[0].EventTime != 100 {
		t.Errorf("review clip event time %.0f, want 100", (*goals)[0].EventTime)
	}
}

func TestRoarFollowedByAScoreChangeIsAGoalNotAReview(t *testing.T) {
	e, goals := reviewEngine(true)
	e.Add(audioSpike(100, 0.9))
	e.Add(scoreUp(108, 0, 0, 1, 0))
	e.Advance(150)
	e.Advance(200)
	if len(*goals) != 1 || (*goals)[0].Review {
		t.Fatalf("want exactly one normal goal, got %+v", *goals)
	}
}

func TestWeakRoarIsNotReviewed(t *testing.T) {
	e, goals := reviewEngine(true)
	e.Add(audioSpike(100, 0.5))
	e.Advance(200)
	if len(*goals) != 0 {
		t.Fatalf("a weak roar should not make a review clip: %+v", *goals)
	}
}

func TestNoReviewBeforeTheScoreboardIsBeingRead(t *testing.T) {
	e, goals := reviewEngine(false)
	e.Add(audioSpike(100, 0.95))
	e.Advance(200)
	if len(*goals) != 0 {
		t.Fatalf("pre-match crowd noise must not be clipped: %+v", *goals)
	}
}

func TestReviewClipsAreSpacedOut(t *testing.T) {
	e, goals := reviewEngine(true)
	e.Add(audioSpike(100, 0.9))
	e.Add(audioSpike(130, 0.9))
	e.Add(audioSpike(200, 0.9))
	e.Advance(300)
	if len(*goals) != 2 {
		t.Fatalf("want 2 review clips (one suppressed by the spacing), got %d", len(*goals))
	}
}

func TestUnfinishedReviewIsDroppedAtTheEndOfTheStream(t *testing.T) {
	e, goals := reviewEngine(true)
	e.Add(audioSpike(100, 0.9))
	e.Flush(110)
	if len(*goals) != 0 {
		t.Fatalf("the score might still have changed, so no review clip: %+v", *goals)
	}
}
