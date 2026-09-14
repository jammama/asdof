package jointips

import "testing"

// §13-3: wr_5 = wr_4 + duration_slots × 30분 (배타적 종료시각)
func TestFormEndTime(t *testing.T) {
	cases := []struct {
		start string
		slots int
		want  string
	}{
		{"12:00", 8, "16:00"},
		{"09:00", 1, "09:30"},
		{"20:30", 1, "21:00"}, // 숨겨진 21:00 센티널 셀이 존재하는 이유
		{"13:00", 8, "17:00"},
		{"09:30", 4, "11:30"},
	}
	for _, c := range cases {
		got, err := FormEndTime(c.start, c.slots)
		if err != nil {
			t.Fatalf("FormEndTime(%q,%d): %v", c.start, c.slots, err)
		}
		if got != c.want {
			t.Errorf("FormEndTime(%q,%d) = %q, want %q", c.start, c.slots, got, c.want)
		}
	}
}

// §3.7: dup_time API 의 wr_5 는 마지막 슬롯의 시작시각(포함)이다 — 의미가 다르다.
func TestDupEndTime(t *testing.T) {
	got, err := DupEndTime("12:00", 8)
	if err != nil {
		t.Fatal(err)
	}
	if got != "15:30" {
		t.Errorf("DupEndTime(12:00,8) = %q, want 15:30", got)
	}
}

// §13-4: 회의실 표기
func TestRoomLabel(t *testing.T) {
	cases := []struct{ place, name, want string }{
		{"현승빌딩 4층", "회의실 A", "현승 4A"},
		{"현승빌딩 4층", "소회의실", "현승 4A"}, // 알파벳이 없으면 A
		{"해성빌딩 7층", "회의실 B", "해성 7B"},
		{"회성빌딩 4층", "8인 회의실-1(회의실B)", "회성 4B"},
		{"회성빌딩 6층", "8인 회의실-2", "회성 6A"},
		{"명우빌딩 2층", "회의실C", "명우 2C"},
		{"해성빌딩 7층", "교육장", "해성 7A"},
	}
	for _, c := range cases {
		if got := RoomLabel(c.place, c.name); got != c.want {
			t.Errorf("RoomLabel(%q,%q) = %q, want %q", c.place, c.name, got, c.want)
		}
	}
}

func TestUsageLine(t *testing.T) {
	want := "• 주간 정기 회의 12:00~16:00"
	if got := UsageLine("주간 정기 회의", "12:00", 8); got != want {
		t.Errorf("UsageLine = %q, want %q", got, want)
	}
}
