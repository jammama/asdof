package jointips

import (
	"strings"
	"testing"
)

// 신청에 실리는 slotTimes 는 시작 시각부터 30분 간격으로 연속해야 한다.
func TestSlotTimes(t *testing.T) {
	got, err := SlotTimes("13:00", 6)
	if err != nil {
		t.Fatal(err)
	}
	want := "13:00,13:30,14:00,14:30,15:00,15:30"
	if strings.Join(got, ",") != want {
		t.Errorf("SlotTimes(13:00,6) = %v, want %s", got, want)
	}
	if _, err := SlotTimes("13:00", 0); err == nil {
		t.Error("칸 수 0 은 거절해야 한다")
	}
	if _, err := SlotTimes("어제", 2); err == nil {
		t.Error("잘못된 시각은 거절해야 한다")
	}
}

func TestEndTime(t *testing.T) {
	cases := []struct {
		start string
		slots int
		want  string
	}{
		{"09:00", 1, "09:30"},
		{"13:00", 6, "16:00"},
		{"09:30", 4, "11:30"},
		{"17:30", 1, "18:00"}, // 마지막 슬롯
	}
	for _, c := range cases {
		got, err := EndTime(c.start, c.slots)
		if err != nil {
			t.Fatalf("EndTime(%q,%d): %v", c.start, c.slots, err)
		}
		if got != c.want {
			t.Errorf("EndTime(%q,%d) = %q, want %q", c.start, c.slots, got, c.want)
		}
	}
}

// 회의실 표기 — 사람이 Notion 에 손으로 쓰던 "현승 5A" 형식에 맞춘다.
func TestRoomLabel(t *testing.T) {
	cases := []struct{ bldgNm, floorNo, spaceNm, want string }{
		{"현승빌딩(S3)", "4", "4층 회의실A", "현승 4A"},
		{"해성빌딩(S1)", "7", "7층 회의실B", "해성 7B"},
		{"명우빌딩(S2)", "2", "2층 회의실C", "명우 2C"},
		{"회성빌딩(S6)", "5", "5층 회의실a", "회성 5A"},
		// floorNo 가 없으면 공간명에서 층을 뽑는다.
		{"현승빌딩(S3)", "", "5층 회의실B", "현승 5B"},
		// 호실 글자가 없는 공간은 억지로 A 를 붙이지 않는다 — 같은 층의 '회의실A' 와 겹친다.
		{"해성빌딩(S1)", "7", "7층 교육장", "해성 7층 교육장"},
		{"회성빌딩(S6)", "2", "2층 회의실", "회성 2층 회의실"},
		// "빌딩"이 안 붙는 건물도 이름을 살린다.
		{"나라키움(S7)", "3", "8인 회의실", "나라키움 8인 회의실"},
		{"", "4", "4층 회의실A", "4층 회의실A"},
	}
	for _, c := range cases {
		if got := RoomLabel(c.bldgNm, c.floorNo, c.spaceNm); got != c.want {
			t.Errorf("RoomLabel(%q,%q,%q) = %q, want %q", c.bldgNm, c.floorNo, c.spaceNm, got, c.want)
		}
	}
}

// 같은 층의 서로 다른 공간이 같은 라벨로 뭉개지면 Notion 기록에서 구분이 안 된다.
func TestRoomLabelDistinguishesSameFloor(t *testing.T) {
	a := RoomLabel("해성빌딩(S1)", "7", "7층 회의실A")
	b := RoomLabel("해성빌딩(S1)", "7", "7층 교육장")
	if a == b {
		t.Errorf("회의실A 와 교육장이 같은 라벨 %q 로 뭉개졌다", a)
	}
}

func TestUsageLine(t *testing.T) {
	want := "• 주간 정기 회의 13:00~16:00"
	if got := UsageLine("주간 정기 회의", "13:00", 6); got != want {
		t.Errorf("UsageLine = %q, want %q", got, want)
	}
}

func TestHours(t *testing.T) {
	if got := Hours(6); got != 3 {
		t.Errorf("Hours(6) = %v, want 3 (1회 한도)", got)
	}
}
