package jointips

import (
	"fmt"
	"strings"
	"testing"
)

// 부록 A 를 그대로 옮긴 픽스처. 시간 셀 24칸 + display:none 센티널 1칸.
func fixture() string {
	var b strings.Builder
	b.WriteString(`<tr data-wr8="41" data-wr6="3" data-wr7="25">` +
		`<td>현승빌딩 4층</td><td>회의실 A</td><td></td><td>4명</td>`)
	for i := 0; i < 24; i++ {
		min := 9*60 + i*30
		tm := fmt.Sprintf("%02d:%02d", min/60, min%60)
		if i == 2 || i == 3 { // 10:00, 10:30 은 기예약
			b.WriteString(fmt.Sprintf(
				`<td class="selected tooltip2" data-time="%s" idx="%d" title="[앰버로드]앰버로드 과제회의" share_id="130785"></td>`, tm, i))
			continue
		}
		b.WriteString(fmt.Sprintf(
			`<td class="available tooltip2" data-time="%s" idx="%d" title="%s"></td>`, tm, i, tm))
	}
	b.WriteString(`<td class="selected" data-time="21:00" style="display:none;"></td></tr>`)

	// rowspan 때문에 빌딩/층 셀이 없는 둘째 행
	b.WriteString(`<tr data-wr8="42" data-wr6="3" data-wr7="25"><td>회의실 B</td><td></td><td>4명</td>`)
	for i := 0; i < 24; i++ {
		min := 9*60 + i*30
		tm := fmt.Sprintf("%02d:%02d", min/60, min%60)
		b.WriteString(fmt.Sprintf(
			`<td class="available tooltip2" data-time="%s" idx="%d" title="%s"></td>`, tm, i, tm))
	}
	b.WriteString(`<td class="selected" data-time="21:00" style="display:none;"></td></tr>`)
	return b.String()
}

// §13-5: 센티널이 기예약으로 세지 않고, 시간 셀이 정확히 24개여야 한다.
func TestParseRows(t *testing.T) {
	rows, err := ParseRows(fixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("행 %d개, 2개여야 함", len(rows))
	}

	r := rows[0]
	if r.RoomID != "41" || r.Building != "3" || r.Floor != "25" {
		t.Errorf("행 식별자 = %s/%s/%s, want 41/3/25", r.RoomID, r.Building, r.Floor)
	}
	if r.Place != "현승빌딩 4층" || r.Name != "회의실 A" || r.Capacity != "4명" {
		t.Errorf("행 내용 = %q/%q/%q", r.Place, r.Name, r.Capacity)
	}
	if len(r.Cells) != 24 {
		t.Fatalf("시간 셀 %d칸, 24칸이어야 함 (센티널이 섞였나?)", len(r.Cells))
	}
	if last := r.Cells[23]; last.Time != "20:30" {
		t.Errorf("마지막 셀 = %q, want 20:30 (21:00 센티널이 들어옴)", last.Time)
	}
	for _, c := range r.Cells {
		if c.Time == "21:00" {
			t.Fatal("21:00 센티널이 시간 격자에 포함됐다")
		}
	}
	if r.Cells[2].Available || r.Cells[2].Title != "[앰버로드]앰버로드 과제회의" || r.Cells[2].ShareID != "130785" {
		t.Errorf("10:00 셀 파싱 오류: %+v", r.Cells[2])
	}
	if !r.Cells[0].Available || r.Cells[0].Title != "" {
		t.Errorf("available 셀의 title 은 시각이라 회의명이 아니다: %+v", r.Cells[0])
	}

	// rowspan 으로 생략된 위치를 이전 행에서 물려받아야 한다.
	if rows[1].Place != "현승빌딩 4층" || rows[1].Name != "회의실 B" {
		t.Errorf("둘째 행 = %q/%q, 위치를 물려받지 못했다", rows[1].Place, rows[1].Name)
	}
}

// §3.6 2차 판정: 구간이 전부 selected 이고 title 에 회의명이 있어야 성공이다.
func TestOccupied(t *testing.T) {
	rows, _ := ParseRows(fixture())
	r := rows[0]
	if !r.Occupied("10:00", 2, "앰버로드 과제회의") {
		t.Error("10:00~11:00 은 우리 회의명으로 잡혀 있어야 한다")
	}
	if r.Occupied("10:00", 3, "앰버로드 과제회의") {
		t.Error("11:00 은 available 이므로 3슬롯은 성립하지 않는다")
	}
	if r.Occupied("09:00", 1, "앰버로드 과제회의") {
		t.Error("09:00 은 available 이다")
	}
}
