# Tauri + React + Typescript

This template should help get you started developing with Tauri, React and Typescript in Vite.

## Recommended IDE Setup

- [VS Code](https://code.visualstudio.com/) + [Tauri](https://marketplace.visualstudio.com/items?itemName=tauri-apps.tauri-vscode) + [rust-analyzer](https://marketplace.visualstudio.com/items?itemName=rust-lang.rust-analyzer)

## Demo Fleet

`pnpm dev:demo` runs the Console in a plain browser against a scripted Fleet (two Nodes, five Services in mixed states, Spaces, Drives with unsorted files, a device asking to join), so screens can be built and screenshotted without a Node. The same Fleet switches on with `?demo` in the URL or `localStorage.setItem("maitre.demo", "1")`. In the browser console, `demo` is the Fleet: `demo.setReachable(demo.nodes[1].nodeId, false)` pulls Basalt's plug. The script lives in `src/demo/fleet.ts`, the fake transport in `src/demo/transport.ts`.
