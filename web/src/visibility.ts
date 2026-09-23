import { ref } from 'vue'

// One listener for the dashboard, shared by every chart and polling panel.
export const pageVisible = ref(!document.hidden)
document.addEventListener('visibilitychange', () => { pageVisible.value = !document.hidden })
