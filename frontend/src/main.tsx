import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { initEvents } from './events';
import './styles.css';

const container = document.getElementById('root');
if (!container) {
  throw new Error('index.html 缺少 #root 容器');
}

// 事件通道先连：非 Wails 宿主（例如直接用 vite dev 打开浏览器）连不上也不阻塞渲染。
void initEvents();

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
