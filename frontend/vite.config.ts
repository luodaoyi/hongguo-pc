import { defineConfig } from 'vite';

// 不使用 @vitejs/plugin-react：本项目只需 JSX 编译（esbuild 内建），
// 不需要 Fast Refresh，少一个依赖就少一处 CI 风险。
export default defineConfig({
  // Wails 用静态资源 handler 直接伺服 dist，相对 base 避免根路径假设。
  base: './',
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    emptyOutDir: true,
    target: 'es2022',
  },
  server: {
    port: 34115,
    strictPort: true,
  },
});
