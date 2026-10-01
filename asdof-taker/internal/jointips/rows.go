package jointips

import (
	"context"
	"sort"
	"strings"
)

// Row 는 현황 화면의 한 줄 — 공간 하나의 하루치 격자다.
//
// 예전에는 조회 응답 HTML 의 <tr> 을 파싱해 만들었지만, 이제 슬롯 JSON 에서
// 조립한다. 화면·폴백 탐색이 쓰는 모양은 그대로 둬서 위층을 덜 흔든다.
type Row struct {
	SpaceCd  string `json:"space_cd"`
	SpaceNm  string `json:"space_nm"`
	BldgCd   string `json:"bldg_cd"`
	BldgNm   string `json:"bldg_nm"`
	FloorNo  string `json:"floor_no"`
	Capacity int    `json:"capacity"`
	// Bookable 이 false 면 상태정보가 '예약가능'이 아니거나 그 요일에 운영하지 않는다.
	Bookable bool   `json:"bookable"`
	SlotUnit int    `json:"slot_unit"`
	Cells    []Cell `json:"cells"`
}

// Cell 은 30분 한 칸이다.
type Cell struct {
	Time      string `json:"time"` // "09:00"
	End       string `json:"end"`  // "09:30"
	Idx       int    `json:"idx"`  // 행 안에서의 순번
	Available bool   `json:"available"`
	Status    string `json:"status"` // AVAILABLE | UNAVAILABLE | MY_CONFLICT
}

// Place 는 "현승빌딩(S3) 4층" 형태의 위치 표기다.
func (r Row) Place() string {
	if r.FloorNo == "" {
		return r.BldgNm
	}
	return r.BldgNm + " " + r.FloorNo + "층"
}

// Label 은 화면 표시용 이름이다: "현승빌딩(S3) 4층 회의실A".
//
// 공간명이 대개 "4층 회의실A" 처럼 층을 이미 품고 있어서, 그대로 이어 붙이면
// "4층 4층 회의실A" 가 된다. 층이 없는 이름("8인 회의실")에는 층을 살려 준다.
func (r Row) Label() string {
	if r.BldgNm == "" {
		return r.SpaceNm
	}
	if r.FloorNo == "" || strings.HasPrefix(r.SpaceNm, r.FloorNo+"층") {
		return strings.TrimSpace(r.BldgNm + " " + r.SpaceNm)
	}
	return r.Place() + " " + r.SpaceNm
}

// Timetable 은 한 날짜의 공간별 격자를 만든다(현황 조회).
//
// bldgCd 가 비어 있으면 지역의 모든 건물을 훑는다. 건물별로 공간 목록을 받은 뒤
// 슬롯은 건물당 한 번의 slots-batch 로 가져온다 — 공간마다 부르면 왕복이 N배가 된다.
func (c *Client) Timetable(ctx context.Context, regionCd, bldgCd, date string) ([]Row, error) {
	buildings, err := c.Buildings(ctx, regionCd)
	if err != nil {
		return nil, err
	}
	var rows []Row
	for _, b := range buildings {
		if bldgCd != "" && b.BldgCd != bldgCd {
			continue
		}
		spaces, err := c.Spaces(ctx, b.BldgCd)
		if err != nil {
			return nil, err
		}
		byCd := map[string]Space{}
		codes := make([]string, 0, len(spaces))
		for _, s := range spaces {
			byCd[s.SpaceCd] = s
			codes = append(codes, s.SpaceCd)
		}
		batch, err := c.SlotsBatch(ctx, codes, date)
		if err != nil {
			return nil, err
		}
		for _, ss := range batch {
			s := byCd[ss.SpaceCd]
			rows = append(rows, buildRow(b, s, ss))
		}
	}
	sortRows(rows)
	return rows, nil
}

func buildRow(b Building, s Space, ss SpaceSlots) Row {
	unit := ss.SlotUnit
	if unit == 0 {
		unit = s.SlotUnit
	}
	if unit == 0 {
		unit = slotMinutes
	}
	nm := ss.SpaceNm
	if nm == "" {
		nm = s.SpaceNm
	}
	row := Row{
		SpaceCd:  ss.SpaceCd,
		SpaceNm:  nm,
		BldgCd:   b.BldgCd,
		BldgNm:   b.BldgNm,
		FloorNo:  s.FloorNo,
		Capacity: s.Capacity,
		Bookable: ss.DayBookable && s.Bookable(),
		SlotUnit: unit,
	}
	for i, sl := range ss.Slots {
		row.Cells = append(row.Cells, Cell{
			Time: sl.SlotTime,
			End:  sl.EndTime,
			Idx:  i,
			// 운영하지 않는 날/신청을 받지 않는 공간은 칸이 비어 보여도 잡을 수 없다.
			Available: row.Bookable && sl.Free(),
			Status:    sl.Status,
		})
	}
	return row
}

// sortRows 는 건물 → 층 → 공간명 순으로 줄을 세운다. 화면과 폴백 탐색이
// 매번 같은 순서를 보게 해서 결과가 응답 순서에 흔들리지 않게 한다.
func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.BldgCd != b.BldgCd {
			return a.BldgCd < b.BldgCd
		}
		if a.FloorNo != b.FloorNo {
			return floorNum(a.FloorNo) < floorNum(b.FloorNo)
		}
		return a.SpaceNm < b.SpaceNm
	})
}

func floorNum(s string) int {
	n, sign := 0, 1
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "B") || strings.HasPrefix(s, "b") {
		sign, s = -1, s[1:]
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return sign * n
}
