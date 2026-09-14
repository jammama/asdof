package jointips

import (
	"fmt"
	"strings"
	"testing"
)

// row 는 available 패턴 문자열('.'=빈칸, 'x'=예약됨)로 행을 만든다.
func row(id, building, floor, place, name, pattern string) Row {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<tr data-wr8="%s" data-wr6="%s" data-wr7="%s"><td>%s</td><td>%s</td><td></td><td>4명</td>`,
		id, building, floor, place, name))
	for i, ch := range pattern {
		min := 9*60 + i*30
		tm := fmt.Sprintf("%02d:%02d", min/60, min%60)
		cls := "available"
		if ch == 'x' {
			cls = "selected"
		}
		b.WriteString(fmt.Sprintf(`<td class="%s tooltip2" data-time="%s" idx="%d"></td>`, cls, tm, i))
	}
	b.WriteString(`</tr>`)
	rows, err := ParseRows(b.String())
	if err != nil || len(rows) != 1 {
		panic("픽스처 오류")
	}
	return rows[0]
}

const full = "........................" // 24칸 전부 빈칸

func TestMatchPrefersEarlierThenLonger(t *testing.T) {
	// 13:00 = idx 8
	rows := []Row{
		row("41", "3", "25", "현승빌딩 4층", "회의실 A", "xxxxxxxx....xxxxxxxxxxxx"), // 13:00부터 4슬롯
		row("42", "3", "25", "현승빌딩 4층", "회의실 B", "xxxxxxxx........xxxxxxxx"), // 13:00부터 8슬롯
	}
	c := Match(rows, 13*60, 60, 4, 8)
	if c == nil {
		t.Fatal("후보를 찾지 못했다")
	}
	if c.Start() != "13:00" || c.Slots != 8 || c.Row.RoomID != "42" {
		t.Errorf("동률이면 긴 쪽을 골라야 한다: %s %s %d슬롯", c.Row.RoomID, c.Start(), c.Slots)
	}

	// 더 이른 시작이 있으면 길이보다 시작이 우선한다.
	rows = append(rows, row("43", "3", "26", "현승빌딩 6층", "회의실 A", "xxxxxxx.....xxxxxxxxxxxx")) // 12:30부터 5슬롯
	c = Match(rows, 12*60, 60, 4, 8)
	if c == nil || c.Start() != "12:30" || c.Row.RoomID != "43" {
		t.Errorf("이른 시작을 골라야 한다: %+v", c)
	}
}

func TestMatchStartWindowBoundary(t *testing.T) {
	rows := []Row{row("41", "3", "25", "현승빌딩 4층", "회의실 A", "xxxxxxxxxx......xxxxxxxx")} // 14:00부터
	if c := Match(rows, 13*60, 60, 4, 8); c == nil || c.Start() != "14:00" {
		t.Errorf("윈도우 경계(정확히 +60분)는 포함해야 한다: %+v", c)
	}
	if c := Match(rows, 13*60, 30, 4, 8); c != nil {
		t.Errorf("윈도우 밖(+30분)은 제외해야 한다: %+v", c)
	}
}

func TestMatchMinSlots(t *testing.T) {
	rows := []Row{row("41", "3", "25", "현승빌딩 4층", "회의실 A", "xxxxxxxx..xxxxxxxxxxxxxx")} // 13:00부터 2슬롯뿐
	if c := Match(rows, 13*60, 60, 4, 8); c != nil {
		t.Errorf("최소 슬롯 미달은 후보가 아니다: %+v", c)
	}
	if c := Match(rows, 13*60, 60, 2, 8); c == nil || c.Slots != 2 {
		t.Errorf("2슬롯 요구면 잡혀야 한다: %+v", c)
	}
}

func TestMatchCapsAtMaxSlots(t *testing.T) {
	rows := []Row{row("41", "3", "25", "현승빌딩 4층", "회의실 A", full)}
	c := Match(rows, 9*60, 0, 1, 8)
	if c == nil || c.Slots != 8 {
		t.Errorf("최대 슬롯(8)을 넘지 않아야 한다: %+v", c)
	}
}

// §5.2 3단계 탐색: 선호 층 → 같은 빌딩 → 전체
func TestMatchWithPreference(t *testing.T) {
	rows := []Row{
		row("41", "3", "25", "현승빌딩 4층", "회의실 A", "xxxxxxxxxxxxxxxxxxxxxxxx"), // 선호 층인데 꽉 참
		row("43", "3", "26", "현승빌딩 6층", "회의실 A", full),                       // 같은 빌딩 다른 층
		row("7", "2", "12", "해성빌딩 3층", "회의실 A", full),                        // 다른 빌딩
	}
	c := MatchWithPreference(rows, "3", "25", 13*60, 60, 4, 8)
	if c == nil || c.Row.RoomID != "43" {
		t.Errorf("선호 층이 꽉 차면 같은 빌딩의 다른 층으로 내려가야 한다: %+v", c)
	}

	rows[1] = row("43", "3", "26", "현승빌딩 6층", "회의실 A", "xxxxxxxxxxxxxxxxxxxxxxxx")
	c = MatchWithPreference(rows, "3", "25", 13*60, 60, 4, 8)
	if c == nil || c.Row.RoomID != "7" {
		t.Errorf("빌딩 전체가 꽉 차면 전체 탐색으로 내려가야 한다: %+v", c)
	}
}
