# -*- coding: utf-8 -*-
"""원본 저장 화면(정적 HTML)에 왼쪽 컴포넌트 패널용 CSS 오버라이드만 덧붙여
변형 파일을 만든다. 마크업은 건드리지 않는다.

세 파일의 공통 = 배경·아이콘·카드 2단 구조·글자 계층
세 파일의 차이 = 분류 제목에 어떤 방식으로 강조를 주는가
"""
import os

SRC = 'orig.html'
OUT = '/Users/shlee02911/WebstormProjects/oci/asdof-test'

P = '[class*="w-[300px]"][class*="left-2"]'          # 패널 루트
BODY = f'{P} [class*="wf-hide-scrollbar"]'           # 목록 스크롤 영역
CARD = f'{P} [class*="min-h-[96px]"]'                # 컴포넌트 카드
ICONZONE = f'{CARD} > span:first-child'              # 카드 상단 = 아이콘 영역
TEXTZONE = f'{CARD} > span:last-child'               # 카드 하단 = 글자 영역
RESTING = f'{CARD} svg[class*="group-hover:hidden"]' # 정지 상태 아이콘
SEC = f'{P} [class*="bg-[#F3FAFC]"]'                 # 분류 헤더 버튼
SECBAR = f'{SEC} > span:first-child'                 # 분류 헤더 세로 바
SECTXT = f'{P} [class*="text-[#1B8FAD]"]'            # 분류 헤더 글자

# ── 공통 색 ────────────────────────────────────────────────────
SURFACE = '#FBFBFC'   # 패널 목록 배경 — 쨍한 흰색이 아닌 아주 약한 회색
CARD_BG = '#FFFFFF'   # 카드 면 — 배경보다 밝아 카드가 떠 보인다
TEXT_BG = '#FCFCFD'   # 글자 영역 — 흰색에 더 가깝게. 띠가 겨우 보이는 정도
BORDER  = '#F4F5F7'   # 카드 테두리 — 한 단계 더 낮춤
NAME    = '#383F47'   # 컴포넌트 이름 — 제목(#2C3238)보다 한 단계만 연하게
CAPTION = '#2C3238'   # 분류 제목 — 블랙끼가 강한 회색
CHEVRON = '#5A626B'   # 접기 세모 — 제목보다 한 단계 연하게
ICON    = '#A3ABB3'   # 정지 상태 아이콘 — 가장 옅게
SEC_BG  = '#F5F6F7'   # 분류 제목 띠 — 회색
SEC_HOV = '#EFF1F3'   # 분류 제목 띠 호버
HOV_TXT = '#F2FAFD'   # 호버 시 글자 영역


def build(sec_css):
    return f"""
/* == 패널 박스: 각지게, 왼쪽·위 여백 제거 ====================
   원본은 top-2 left-2(8px) 띄워 떠 있는 카드. 좌상단에 붙여 도킹시킨다.
   rounded-xl은 카드에도 쓰이므로 패널 껍데기(직계 자식)만 겨냥한다. */
{P}{{top:2px !important;left:0 !important}}
{P} > [class*="rounded-xl"]{{border-radius:0 !important}}
{P} [class*="rounded-b-xl"]{{border-radius:0 !important}}

/* == 검색 박스: 각지게 + 높이 40px -> 34px ================== */
{P} [class*="h-10"]{{border-radius:0 !important;height:34px !important}}

/* == 패널 헤더('컴포넌트 추가'): 좌측 시작점을 12px로 ========
   원본 p-2(8px)라 헤더 아이콘만 8px에서 시작하고
   검색박스·탭·분류제목·카드는 전부 12px에서 시작했다. 12px로 통일. */
{P} > div > button[aria-expanded]{{padding:10px 12px !important}}

/* == 캔버스 노드 카드 =======================================
   (a) 상·하단을 가르는 border-t 제거 -> 하단에 옅은 회색 면
   (b) 높이 보정: 56 + 1(border) + 32 = 89 인데 카드는 92px이라
       하단 내용이 3px 위로 쏠려 있었다(위 1.5px / 아래 5.5px).
       하단을 36px로 채워 '성공' 태그·시각·아이콘을 한 줄에 정렬. */
.react-flow__node [class*="h-[32px]"][class*="border-t"]{{
  border-top:0 !important;
  background:#F7F8FA !important;
  height:36px !important;
  border-radius:0 0 6px 6px !important;
}}

/* (c) 그림자 -> 옅고 얕은 회색 테두리.
   그림자는 별도의 absolute 오버레이 레이어라, 거기에 테두리를 그리면
   카드 레이아웃에 전혀 영향을 주지 않는다. */
.react-flow__node [class*="inset-0"][class*="z-30"]{{
  box-shadow:none !important;
  border:1px solid #E5E8EB !important;
}}

/* (d) 내부 패딩 8px -> 12px.
   하단 줄은 원래 폭 224px 중 216px을 채워(96%) 쏟아질 듯 보였다.
   아이콘 버튼을 28 -> 24px로 줄여 늘어난 패딩만큼의 여유를 만든다. */
.react-flow__node [class*="h-[56px]"],
.react-flow__node [class*="h-[32px]"][class*="border-t"]{{
  padding-left:12px !important;padding-right:12px !important;
}}
.react-flow__node [class*="h-[32px]"][class*="border-t"] [class*="w-[28px]"]{{
  width:24px !important;height:24px !important;
}}

/* (e) '성공' 태그 테두리 제거 — 1px는 투명으로 남겨 크기는 그대로 둔다 */
.react-flow__node [class*="h-[32px]"][class*="border-t"] span[class*="rounded-full"]{{
  border-color:transparent !important;
}}

/* (f) 시각: 조금 작게, 대신 상단바 '마지막 업데이트' 날짜(#213547)만큼 진하게 */
.react-flow__node [class*="h-[32px]"][class*="border-t"] p{{
  font-size:11px !important;color:#213547 !important;
}}

/* == 패널 배경: 쨍한 흰색 -> 아주 약한 회색 ==================
   카드(흰색)가 배경 위에 떠 보이게 만든다. */
{BODY}{{background:{SURFACE} !important;padding-top:0 !important}}

/* == 아이콘: 쉬고 있을 때만 무채색 ===========================
   호버용 아이콘(#2BB0D5)은 손대지 않는다 - 가리킨 것만 색이 돌아온다. */
{RESTING} [stroke]{{stroke:{ICON} !important}}
{RESTING} [fill="#222222"]{{fill:{ICON} !important}}
{RESTING} > rect[fill="#F9F9F9"]{{fill:transparent !important}}

/* == 카드: 아이콘 영역과 글자 영역을 위아래로 고정 분할 ======
   원본은 justify-center라 이름이 1줄이냐 2줄이냐에 따라 아이콘이
   위아래로 흔들린다. 영역 높이를 고정해 아이콘 위치를 맞춘다.
   두 영역 사이에 선은 두지 않는다 - 배경색 차이로만 구분. */
{CARD}{{
  justify-content:flex-start !important;
  padding:0 !important;
  overflow:hidden !important;
  background:{CARD_BG} !important;
  border-color:{BORDER} !important;
  border-radius:6px !important;
  box-shadow:none !important;
}}
{ICONZONE}{{
  height:50px !important; width:100% !important; margin:0 !important;
  display:flex !important; align-items:center !important; justify-content:center !important;
}}
{TEXTZONE}{{
  flex:1 1 auto !important; width:100% !important; margin:0 !important;
  display:flex !important; align-items:center !important; justify-content:center !important;
  padding:6px 8px !important;
  background:{TEXT_BG} !important;
  border:0 !important;
}}

/* == 컴포넌트 이름 == */
{P} [class*="line-clamp-2"]{{color:{NAME} !important;visibility:visible !important}}
/* ↑ 저장본 한계 보정: 원본은 호버 시 이름을 숨기고 '전체 이름 오버레이'를 띄우는데,
   SingleFile이 그 오버레이에 sf-hidden(display:none!important)을 박아 저장했다.
   그대로 두면 호버할 때 이름이 사라지므로 계속 보이게 고정한다. */

/* == 분류 헤더: 패널 폭을 꽉 채우는 각진 띠 ==================
   스크롤 영역의 px-2.5(10px)를 음수 마진으로 상쇄해 좌우 여백을 없앤다.
   글자는 카드 왼쪽 끝(12px)에 맞춘다. */
{SEC}{{background:{SEC_BG} !important;min-height:36px !important;
  margin-left:-10px !important;width:calc(100% + 20px) !important;
  padding:12px 12px 8px 12px !important;border-radius:0 !important;
  position:relative !important;align-items:flex-end !important}}
{SEC}:hover{{background:{SEC_HOV} !important}}
{SEC} > svg{{align-self:center !important}}
{SEC} svg path{{fill:{CHEVRON} !important}}
/* 제목 띠는 위와 멀고 아래와 가깝게 — 아래 카드들과 한 묶음으로 읽히도록.
   원본은 위 12px / 아래 8px 이라 오히려 위쪽에 붙어 보였다. */
{P} section{{margin-bottom:0 !important}}
{P} section + section{{padding-top:10px !important}}
{P} [class*="pt-2"][class*="px-0.5"]{{padding-top:6px !important}}
{sec_css}
/* == 호버: 색은 가리킨 카드에만 == */
{CARD}:hover{{border-color:#DFE7EB !important;background:{CARD_BG} !important;
  box-shadow:none !important}}
{CARD}:hover > span:last-child{{background:{HOV_TXT} !important}}
{P} span[class*="group-hover:block"][class*="bg-[#F0FBFF]"]{{
  background:{HOV_TXT} !important;color:{NAME} !important}}
"""


TITLE = f"""
{SECTXT}{{color:{CAPTION} !important;font-size:13px !important;
  font-weight:700 !important;letter-spacing:.01em !important}}"""

VARIANTS = {
    # A. 띠만 — 마커 없이 배경 띠와 글자로만
    'MinerReport_design2.html': dict(
        title='Miner Report - 제목: 띠만',
        sec=TITLE + f"""
{SECBAR}{{display:none !important}}"""),

    # B. 띠 + 왼쪽 가장자리 세로선 — 띠가 풀블리드라 가장자리에 붙는다
    'MinerReport_design3.html': dict(
        title='Miner Report - 제목: 띠 + 왼쪽 가장자리 선',
        sec=TITLE + f"""
/* 선 시작점 11px = 카드 왼쪽 시작점(12px)보다 1px 왼쪽 */
{SEC}{{padding-left:21px !important}}
{SECBAR}{{display:block !important;position:absolute !important;
  left:11px !important;top:0 !important;bottom:0 !important;
  width:2px !important;height:auto !important;
  background:#2BB0D5 !important;border-radius:0 !important;margin:0 !important}}"""),

    # C. 띠 + 글자 앞 작은 마커
    'MinerReport_design4.html': dict(
        title='Miner Report - 제목: 띠 + 작은 마커',
        sec=TITLE + f"""
/* 마커 시작점 11px = 카드 왼쪽 시작점(12px)보다 1px 왼쪽.
   11 + 마커 2 + gap 8 = 글자 21px */
{SEC}{{padding-left:11px !important;gap:8px !important}}
{SECBAR}{{display:block !important;width:2px !important;height:10px !important;
  background:#2BB0D5 !important;border-radius:1px !important;
  margin:0 !important;flex-shrink:0 !important}}"""),
}

src = open(SRC, encoding='utf-8').read()
assert '<title>Miner Report</title>' in src

for name, v in VARIANTS.items():
    css = build(v['sec'])
    out = src.replace('<title>Miner Report</title>',
                      f'<title>{v["title"]}</title>', 1)
    out += f'\n<style data-wf-variant="{name}">{css}</style>\n'
    for d in (OUT, os.path.dirname(os.path.abspath(SRC))):   # 배포본 + 미리보기용 사본
        open(os.path.join(d, name), 'w', encoding='utf-8').write(out)
    print(f'{name:32s} {len(out):>9,} bytes   {v["title"]}')
