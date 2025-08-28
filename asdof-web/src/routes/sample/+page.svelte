<script>
    import VideoRoom from "../VideoRoom.svelte";

    export let meetingId = "ABCD-1234";
    export let participants = [
        { name: "민지", role: "host", img: "https://images.pexels.com/photos/220453/pexels-photo-220453.jpeg", active: true, aspect: "landscape" },
        { name: "지훈", role: "participant", img:     "https://images.pexels.com/photos/415829/pexels-photo-415829.jpeg", active: false, aspect: "portrait" },
        { name: "수아", role: "participant", img:     "https://images.pexels.com/photos/712513/pexels-photo-712513.jpeg", active: false, aspect: "landscape" },
    ];
    export let messages = [
        { from: "민지", text: "안녕하세요 — 오늘 자료는 오른쪽에 올려둘게요." },
        { from: "지훈", text: "잘 들려요!" },
    ];

    let chatInput = "";
    const sendMessage = () => {
        if (!chatInput.trim()) return;
        messages = [...messages, { from: "나", text: chatInput }];
        chatInput = "";
    };
</script>

<div class="app" role="application" aria-label="회의 진행중">
    <header>
        <div class="brand">
            <div class="logo" aria-hidden>둥</div>
            <div>
                <div class="title">회의 진행중</div>
                <div class="meeting-id">회의 ID: <strong>{meetingId}</strong></div>
            </div>
        </div>
        <div class="controls" role="toolbar" aria-label="회의 제어">
            <button class="btn" title="채팅 열기">💬</button>
            <button class="btn primary" title="화면 공유">📤</button>
        </div>
    </header>

    <main class="stage" id="stage">
        <VideoRoom {participants} />

        <div class="stage-footer">
            <div></div>
            <div class="controls" role="toolbar" aria-label="로컬 제어">
                <button class="btn" aria-pressed="false" title="마이크 토글">🎙️</button>
                <button class="btn" aria-pressed="true" title="비디오 토글">📷</button>
                <button class="btn warn" title="회의 나가기">📴</button>
            </div>
        </div>
    </main>

    <aside class="chat" aria-live="polite">
        <div class="section-title">채팅</div>
        <div class="chat-list" role="log">
            {#each messages as m}
                <div class="chat-msg"><strong>{m.from}</strong> {m.text}</div>
            {/each}
        </div>
        <form on:submit|preventDefault={sendMessage} class="chat-form">
            <label for="chat-input" class="sr-only">메시지 입력</label>
            <input id="chat-input" type="text" bind:value={chatInput} placeholder="메시지 보내기..." />
            <button class="btn" type="submit" title="전송" style="--size:44px">➤</button>
        </form>
    </aside>
</div>

<style lang="scss">

  :root {
    --bg: #0f1724;
    --card: rgba(255, 255, 255, 0.04);
    --muted: rgba(255, 255, 255, 0.6);
    --accent: #7c5cff;
    --accent-2: #45d6a3;
    --glass: rgba(255, 255, 255, 0.06);
    --radius: 18px;
    --gap: 12px;
    overflow: hidden;
  }
  body {
    margin: 0;
    background: radial-gradient(1200px 600px at 10% 10%, rgba(124, 92, 255, 0.10), transparent),
    linear-gradient(180deg, #071226 0%, var(--bg) 100%);
    color: #fff;
    padding: 24px;

    font-family: Inter, ui-sans-serif, system-ui;
  }

  .app {
    width: 1100px;
    padding: 1rem;
    min-width: 300px;
    max-width: calc(100% - 48px);
    min-height: 440px;
    height: 90vh;
    border-radius: 24px;
    background: linear-gradient(180deg, rgba(255, 255, 255, 0.02), rgba(255, 255, 255, 0.01));
    box-shadow: 0 10px 30px rgba(2, 6, 23, 0.6), inset 0 1px 0 rgba(255, 255, 255, 0.02);
    display: flex;
    flex-direction: column;
    gap: 18px;
    overflow: auto;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    .meeting-id { font-size: 13px; color: var(--muted); }
    .brand {
      display: flex;
      gap: 12px;
      align-items: center;

      .logo {
        width: 56px;
        height: 56px;
        border-radius: 50%;
        display: grid;
        place-items: center;
        background: linear-gradient(135deg, var(--accent), var(--accent-2));
        font-weight: 700;
        font-size: 18px;
        color: white;
      }
    }
  }

  .stage {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 12px;

    .layout {
      display: flex;
      flex: 1;
      gap: 12px;
      flex-wrap: wrap;

      .participants-list {
        flex: 1;
        display: flex;
        flex-direction: column;
        gap: 8px;

        .participant-video {
          position: relative;
          border-radius: 14px;
          overflow: hidden;
          background: var(--glass);
          border: 1px solid rgba(255,255,255,0.03);
          flex: 1;
          &.landscape img { aspect-ratio: 16/9; object-fit: cover; }
          &.portrait img { aspect-ratio: 9/16; object-fit: cover; }
          .overlay { position: absolute; bottom: 6px; left: 6px; background: rgba(0,0,0,0.5); padding: 4px 8px; border-radius: 8px; font-size: 12px; }
        }
      }

      .presenter {
        flex: 2;
        display: flex;
        align-items: center;
        justify-content: center;
        .presenter-video {
          position: relative;
          width: 100%;
          height: 100%;
          border-radius: 16px;
          overflow: hidden;
          background: var(--glass);
          &.landscape img { aspect-ratio: 16/9; object-fit: cover; width: 100%; }
          &.portrait img { aspect-ratio: 9/16; object-fit: cover; height: 100%; }
          .overlay { position: absolute; bottom: 10px; left: 10px; background: rgba(0,0,0,0.5); padding: 6px 12px; border-radius: 10px; }
        }
      }
    }
  }

  .stage-footer {
    display: flex;
    justify-content: space-between;
    align-items: center;

  }

  .chat {
    border-radius: 16px;
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;

    .chat-list { max-height: 160px; overflow: auto; display: flex; flex-direction: column; gap: 8px; }
    .chat-msg { background: rgba(255,255,255,0.02); padding: 8px; border-radius: 10px; font-size: 13px; }
    .chat-form { display: flex; gap: 8px; input { flex: 1; border-radius: 999px; padding: 10px; background: transparent; color: inherit; border: 1px solid rgba(255,255,255,0.03);} }
  }

  .controls {
    display: flex;
    gap: 10px;
    background: rgba(255,255,255,0.02);
    border-radius: 999px;
    padding: 10px;

    .btn {
      --size: 44px;
      width: var(--size);
      height: var(--size);
      border-radius: 999px;
      background: rgba(255,255,255,0.03);
      border: none;
      cursor: pointer;
      &.primary { background: linear-gradient(135deg, var(--accent), var(--accent-2)); }
      &.warn { background: linear-gradient(135deg,#ff6b6b,#ff9a8b); }
    }
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0,0,0,0);
  }
</style>
