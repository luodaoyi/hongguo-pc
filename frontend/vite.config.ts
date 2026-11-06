import { defineConfig } from 'vite';

// 不使用 @vitejs/plugin-react：本项目只需 JSX 编译（esbuild 内建），
// 不需要 Fast Refresh，少一个依赖就少一处 CI 风险。
export default defineConfig(({ mode }) => ({
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
  resolve: mode === 'development'
    ? {
        // 浏览器实时预览（npm run dev）：伪造 Wails 运行时返回假数据，
        // 生产构建（npm run build）不经过此 alias，产物不受影响。
        alias: [{ find: /^@wailsio\/runtime$/, replacement: '/mock/runtime.ts' }],
      }
    : {},
}));
