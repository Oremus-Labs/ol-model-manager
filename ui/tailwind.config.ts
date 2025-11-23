import type { Config } from 'tailwindcss';

const config: Config = {
  content: [
    './app/**/*.{js,ts,jsx,tsx}',
    './components/**/*.{js,ts,jsx,tsx}',
  ],
  theme: {
    extend: {
      colors: {
        brand: {
          50: '#f1f5ff',
          100: '#dfe8ff',
          200: '#b9ceff',
          500: '#2563eb',
          600: '#1d4ed8',
          700: '#1e3a8a'
        }
      },
      boxShadow: {
        card: '0 20px 45px rgba(15,23,42,0.35)'
      }
    },
  },
  plugins: [],
};

export default config;
