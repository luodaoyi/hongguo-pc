// scripts/release-notes-fill.mjs —— 发版辅助：把 .release-notes.md 里的
// 「更新说明（可直接复制给用户）」代码块替换进 GitHub Release 正文的对应节。
// 用法：node scripts/release-notes-fill.mjs <tag>
//   读取 .release-notes.md（本地手写的分类要点）整体替换 Release 正文的
//   「## 更新说明」到下一个「## 」之间；没有该节则报错退出。
import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const tag = process.argv[2];
if (!tag) {
  console.error('usage: node scripts/release-notes-fill.mjs <tag>');
  process.exit(2);
}
const notesFile = path.resolve('.release-notes.md');
if (!fs.existsSync(notesFile)) {
  console.error(`missing ${notesFile}（按分类写好要点后再跑）`);
  process.exit(1);
}
const notes = fs.readFileSync(notesFile, 'utf8').trimEnd();
if (!notes.startsWith('## 更新说明')) {
  console.error('.release-notes.md 必须以「## 更新说明」开头');
  process.exit(1);
}

const body = execSync(`gh release view ${tag} --json body --jq .body`, {
  encoding: 'utf8',
  maxBuffer: 16 * 1024 * 1024,
});
const start = body.indexOf('## 更新说明');
const rest = body.indexOf('\n## ', start + 1);
if (start < 0 || rest < 0) {
  console.error('release body 缺「更新说明」节或后续分节标记，请检查 release.yml 模板');
  process.exit(1);
}
const next = body.slice(0, start) + notes + '\n\n' + body.slice(rest + 1);
const tmp = path.join(process.env.TEMP ?? '.', `hg-notes-${Date.now()}.md`);
fs.writeFileSync(tmp, next);
execSync(`gh release edit ${tag} --notes-file "${tmp}"`, { stdio: 'inherit' });
fs.rmSync(tmp, { force: true });
console.log(`ok: ${tag} 的更新说明已替换（来源 .release-notes.md）`);
