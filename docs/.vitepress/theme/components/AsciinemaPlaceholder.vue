<script setup lang="ts">
defineProps<{
  label?: string
}>()
</script>

<template>
  <div class="asciinema-placeholder">
    <div class="asciinema-content">
      <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <polygon points="5 3 19 12 5 21 5 3" />
      </svg>
      <span class="asciinema-label">{{ label || 'Terminal Recording' }}</span>
      <span class="asciinema-hint">Replace with asciinema embed: &lt;script src="https://asciinema.org/a/YOUR_ID.js" id="asciicast-YOUR_ID" async&gt;&lt;/script&gt;</span>
    </div>
  </div>
</template>

<style scoped>
.asciinema-placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 350px;
  border-radius: 8px;
  border: 2px dashed #3b3d57;
  background: #16161e;
  margin: 16px 0;
}

.asciinema-content {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  color: #565a89;
  padding: 24px;
  text-align: center;
}

.asciinema-label {
  font-size: 14px;
  font-weight: 500;
}

.asciinema-hint {
  font-size: 11px;
  font-family: monospace;
  max-width: 400px;
  opacity: 0.7;
}
</style>
