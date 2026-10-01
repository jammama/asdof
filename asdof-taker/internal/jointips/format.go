package jointips

import (
	"fmt"
	"regexp"
	"strings"
)

const slotMinutes = 30

// SlotTimes 는 시작 시각과 칸 수로 신청에 넣을 slotTimes 배열을 만든다.
//
//	("13:00", 4) → ["13:00","13:30","14:00","14:30"]
//
// 사이트는 연속된 칸만 받으므로 중간에 구멍이 생기지 않게 여기서 한 번에 만든다.
func SlotTimes(start string, slots int) ([]string, error) {
	m, err := parseHHMM(start)
	if err != nil {
		return nil, err
	}
	if slots < 1 {
		return nil, fmt.Errorf("칸 수가 1보다 작습니다: %d", slots)
	}
	out := make([]string, 0, slots)
	for i := 0; i < slots; i++ {
		out = append(out, formatHHMM(m+i*slotMinutes))
	}
	return out, nil
}

// EndTime 은 마지막 칸의 끝 시각이다. ("13:00", 4) → "15:00"
func EndTime(start string, slots int) (string, error) {
	m, err := parseHHMM(start)
	if err != nil {
		return "", err
	}
	return formatHHMM(m + slots*slotMinutes), nil
}

// TimeRange 는 사람이 읽는 예약 시간 표기다: "13:00~15:00".
func TimeRange(start string, slots int) string {
	end, err := EndTime(start, slots)
	if err != nil {
		return start
	}
	return start + "~" + end
}

// Hours 는 칸 수를 시간으로 바꾼다. 사이트의 한도(1회 3시간 / 1일 5시간)와 맞춰 보는 값.
func Hours(slots int) float64 { return float64(slots) * slotMinutes / 60 }

var (
	// "현승빌딩(S3)" → "현승", "나라키움(S7)" → "나라키움"
	bldgRe = regexp.MustCompile(`^\s*([^(]+?)(?:빌딩)?\s*(?:\(|$)`)
	// "4층 회의실A" → 층 "4", 호실 "A". 호실 글자는 '회의실' 바로 뒤에 붙은 것만 인정한다 —
	// "8인 회의실"/"교육장" 같은 이름에서 아무 알파벳이나 주워 오면 엉뚱한 라벨이 된다.
	floorRe = regexp.MustCompile(`(\d+)\s*층`)
	roomRe  = regexp.MustCompile(`회의실\s*([A-Za-z])`)
)

// RoomLabel 은 Notion 에 넣을 회의실 표기를 만든다: "현승 4A".
//
//	빌딩 짧은이름 + 층숫자 + 호실 알파벳
//
// 사람이 손으로 쓰던 표기를 그대로 맞추는 것이 목적이다. 호실 글자가 없는 공간
// (교육장·세미나실·"8인 회의실")은 억지로 A 를 붙이지 않고 이름을 그대로 쓴다 —
// 같은 층의 "회의실A" 와 라벨이 겹쳐 버리기 때문이다.
func RoomLabel(bldgNm, floorNo, spaceNm string) string {
	building := strings.TrimSpace(bldgNm)
	if m := bldgRe.FindStringSubmatch(bldgNm); m != nil {
		building = strings.TrimSpace(m[1])
		building = strings.TrimSuffix(building, "빌딩")
	}
	floor := strings.TrimSpace(floorNo)
	if floor == "" {
		if m := floorRe.FindStringSubmatch(spaceNm); m != nil {
			floor = m[1]
		}
	}
	room := ""
	if m := roomRe.FindStringSubmatch(spaceNm); m != nil {
		room = strings.ToUpper(m[1])
	}
	switch {
	case building == "":
		return strings.TrimSpace(spaceNm)
	case floor != "" && room != "":
		return fmt.Sprintf("%s %s%s", building, floor, room)
	default:
		return strings.TrimSpace(building + " " + strings.TrimSpace(spaceNm))
	}
}

// UsageLine 은 Notion 의 "사용 현황" 한 줄이다. 불릿은 U+2022 다.
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
