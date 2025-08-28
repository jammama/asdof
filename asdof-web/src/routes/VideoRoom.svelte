<script lang="ts">
    export let host;
    export let participants: [{
        name: string,
        role: "host" | "participant",
        img: string,
    }];
</script>

<div class="video-room">
    <div class="host">
        <img src={participants.find(p => p.role === 'host')?.img ?? ''} alt="Host video" />
    </div>
    <div class="participants">
        {#each participants.filter(p => p.role === 'participant') as p}
            <div class="participant">
                <img src={p.img} alt="Participant video" />
            </div>
        {/each}
    </div>
</div>

<style lang="scss">
  .video-room {
    display: flex;
    flex-wrap: wrap;
    gap: 1rem;
    justify-content: center;
    align-items: stretch;
  }

  .host {
    flex: 1.5 1 ;
    max-width: 200px;

    img {
      width: 100%;
      height: auto;
      border-radius: 1rem;
      object-fit: cover;
    }
  }

  .participants {
    flex: 1 1;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    justify-content: center;
    align-items: flex-start;

    .participant {
      flex: 1 1 100px;
      max-width: 150px;

      img {
        width: 100%;
        border-radius: 0.75rem;
        object-fit: cover;
      }
    }
  }
</style>