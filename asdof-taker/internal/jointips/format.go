package jointips

import (
	"fmt"
	"regexp"
	"strings"
)

const slotMinutes = 30

// FormEndTime 은 예약 폼의 wr_5 를 계산한다.
//
// ★ 최대 함정(§3.5): wr_5 는 배타적(exclusive) 종료시각이다. 사이트 JS 가
// `$(".selection:last").next().attr("data-time")` 를 넣기 때문에,
// 마지막 선택 셀의 "다음 칸" 시각이 들어간다.
//
//	wr_5 = wr_4 + slots × 30분   ("12:00", 8) → "16:00"
func FormEndTime(start string, slots int) (string, error) {
	m, err := parseHHMM(start)
	if err != nil {
		return "", err
	}
	return formatHHMM(m + slots*slotMinutes), nil
}

// DupEndTime 은 겹침 확인 API(std=dup_time)의 wr_5 다.
//
// 같은 이름이지만 의미가 다르다(§3.7): 여기서는 "마지막 선택 슬롯의 시작시각(포함)"이
// 들어간다. 12:00~16:00 예약이면 15:30 이다.
func DupEndTime(start string, slots int) (string, error) {
	m, err := parseHHMM(start)
	if err != nil {
		return "", err
	}
	return formatHHMM(m + (slots-1)*slotMinutes), nil
}

// TimeRange 는 사람이 읽는 예약 시간 표기다: "12:00~16:00".
func TimeRange(start string, slots int) string {
	end, err := FormEndTime(start, slots)
	if err != nil {
		return start
	}
	return start + "~" + end
}

var (
	placeRe = regexp.MustCompile(`^\s*(\S+?)빌딩\s*(\d+)\s*층`)
	roomRe  = regexp.MustCompile(`[A-Da-d]`)
)

// RoomLabel 은 Notion 에 넣을 회의실 표기를 만든다(§7.4): "현승 4A".
//
//	빌딩 짧은이름 + 층숫자 + 호실 알파벳(A~D, 없으면 A)
func RoomLabel(place, name string) string {
	building, floor := "", ""
	if m := placeRe.FindStringSubmatch(place); m != nil {
		building, floor = m[1], m[2]
	}
	room := "A"
	if m := roomRe.FindString(name); m != "" {
		room = strings.ToUpper(m)
	}
	if building == "" && floor == "" {
		return strings.TrimSpace(name)
	}
	return fmt.Sprintf("%s %s%s", building, floor, room)
}

// UsageLine 은 Notion 의 "사용 현황" 한 줄이다(§7.3). 불릿은 U+2022 다.
func UsageLine(subject, start string, slots int) string {
	return "• " + subject + " " + TimeRange(start, slots)
}

func parseHHMM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil {
		return 0, fmt.Errorf("시각 형식이 잘못됨: %q", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("시각 범위를 벗어남: %q", s)
	}
	return h*60 + m, nil
}

func formatHHMM(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }
