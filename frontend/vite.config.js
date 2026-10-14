import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import {fileURLToPath, URL} from 'node:url';
export default defineConfig({plugins:[vue()],base:'./',build:{outDir:'dist',emptyOutDir:true,rollupOptions:{input:{desktop:fileURLToPath(new URL('./index.html',import.meta.url)),phone:fileURLToPath(new URL('./phone.html',import.meta.url))}}}});
