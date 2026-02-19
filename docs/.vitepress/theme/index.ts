import DefaultTheme from 'vitepress/theme'
import './tailwind.css'
import HomeLayout from './HomeLayout.vue'
import PlaceholderImage from './components/PlaceholderImage.vue'
import AsciinemaPlaceholder from './components/AsciinemaPlaceholder.vue'
import type { Theme } from 'vitepress'

export default {
  extends: DefaultTheme,
  Layout: HomeLayout,
  enhanceApp({ app }) {
    app.component('PlaceholderImage', PlaceholderImage)
    app.component('AsciinemaPlaceholder', AsciinemaPlaceholder)
  }
} satisfies Theme
