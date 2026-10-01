package jointips

import "testing"

// row 는 "01110…" 같은 문자열로 격자를 만든다. 1 = 신청 가능.
func row(spaceCd, bldgCd, floorNo string, pattern string) Row {
	r := Row{SpaceCd: spaceCd, BldgCd: bldgCd, FloorNo: floorNo, BldgNm: "테스트빌딩", SpaceNm: spaceCd,
		Bookable: true, SlotUnit: 30}
	min := 9 * 60
	for i, ch := range pattern {
		r.Cells = append(r.Cells, Cell{
			Time:      formatHHMM(min + i*30),
			End:       formatHHMM(min + (i+1)*30),
			Idx:       i,
			Available: ch == '1',
			Status:    map[bool]string{true: SlotAvailable, false: SlotUnavailable}[ch == '1'],
		})
	}
	return r
}

// 09:00 부터 30분 간격. 13:00 은 인덱스 8.
func TestMatchPrefersEarlierThenLonger(t *testing.T) {
	rows := []Row{
		row("A", "B1", "4", "000000001111000000"), // 13:00 부터 4칸
		row("B", "B1", "5", "000000001111110000"), // 13:00 부터 6칸
		row("C", "B1", "6", "000000000011111100"), // 14:00 부터 6칸
	}
	// 13:00 ±60분, 최소 2칸, 최대 6칸
	c := Match(rows, 13*60, 60, 2, 6)
	if c == nil {
		t.Fatal("후보를 찾지 못했다")
	}
	if c.Start() != "13:00" {
		t.Errorf("시작 = %s, want 13:00", c.Start())
	}
	// 같은 시작이면 더 긴 쪽
	if c.Row.SpaceCd != "B" || c.Slots != 6 {
		t.Errorf("후보 = %s %d칸, want B 6칸", c.Row.SpaceCd, c.Slots)
	}
}

// 한 번에 잡을 수 있는 길이를 넘겨서 가져오면 사이트가 거절한다.
func TestMatchCapsAtMaxSlots(t *testing.T) {
	rows := []Row{row("A", "B1", "4", "111111111111111111")}
	c := Match(rows, 9*60, 0, 2, 6)
	if c == nil {
		t.Fatal("후보를 찾지 못했다")
	}
	if c.Slots != 6 {
		t.Errorf("칸 수 = %d, want 6 (1회 한도)", c.Slots)
	}
}

func TestMatchSkipsShortRuns(t *testing.T) {
	rows := []Row{row("A", "B1", "4", "000000001100000000")} // 13:00 부터 2칸뿐
	if c := Match(rows, 13*60, 0, 4, 6); c != nil {
		t.Errorf("최소 4칸인데 %d칸짜리를 골랐다", c.Slots)
	}
}

// 운영 요일이 아니거나 상태가 '예약가능'이 아닌 공간은 칸이 비어 보여도 잡을 수 없다.
func TestMatchSkipsUnbookableRow(t *testing.T) {
	r := row("A", "B1", "4", "111111111111111111")
	r.Bookable = false
	if c := Match([]Row{r}, 9*60, 0, 2, 6); c != nil {
		t.Errorf("신청을 받지 않는 공간을 골랐다: %s", c.Row.SpaceCd)
	}
}

// 선호는 건물 → 층 → 전체 순으로 완화된다.
func TestMatchWithPreference(t *testing.T) {
	rows := []Row{
		row("other", "B2", "3", "111111111111111111"),
		row("wantFloor", "B1", "5", "000000001111110000"),
		row("otherFloor", "B1", "4", "111111111111111111"),
	}
	// 1단계: 건물 B1 + 5층
	c := MatchWithPreference(rows, "B1", "5", 13*60, 60, 2, 6)
	if c == nil || c.Row.SpaceCd != "wantFloor" {
		t.Fatalf("1단계 = %v, want wantFloor", c)
	}
	// 2단계: 선호 층에 자리가 없으면 같은 건물의 다른 층
	c = MatchWithPreference(rows, "B1", "9", 9*60, 0, 2, 6)
	if c == nil || c.Row.BldgCd != "B1" {
		t.Fatalf("2단계 = %v, want B1 의 다른 층", c)
	}
	// 3단계: 건물에도 없으면 전체
	c = MatchWithPreference(rows, "B9", "", 9*60, 0, 2, 6)
	if c == nil {
		t.Fatal("3단계에서 전체 탐색으로 내려가지 않았다")
	}
}

// Occupied 는 그 구간이 통째로 차 있는지만 본다(누가 잡았는지는 슬롯 API 가 알려주지 않는다).
func TestOccupied(t *testing.T) {
	r := row("A", "B1", "4", "000000000000111100") // 09:00~15:00 차 있고, 15:00~17:00 은 빈다
	if !r.Occupied("09:00", 4) {
		t.Error("09:00~11:00 은 UNAVAILABLE 이므로 차 있다고 봐야 한다")
	}
	if r.Occupied("15:00", 4) {
		t.Error("15:00~17:00 은 available 이므로 비어 있다")
	}
}

func TestRowLabel(t *testing.T) {
	// 공간명이 층을 이미 품고 있으면 겹쳐 쓰지 않는다.
	r := Row{BldgNm: "현승빌딩(S3)", FloorNo: "5", SpaceNm: "5층 회의실A"}
	if got := r.Label(); got != "현승빌딩(S3) 5층 회의실A" {
		t.Errorf("Label = %q", got)
	}
	// 층이 없는 이름에는 층을 살린다.
	r2 := Row{BldgNm: "나라키움(S7)", FloorNo: "3", SpaceNm: "8인 회의실"}
	if got := r2.Label(); got != "나라키움(S7) 3층 8인 회의실" {
		t.Errorf("Label = %q", got)
	}
}
