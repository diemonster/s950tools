// MOCK wailsjs/runtime/runtime — only loaded when MOCK_WAILS=1.
// We don't need real event flow for screenshotting; emit nothing.

const listeners = new Map<string, Set<(...data: any) => void>>();

export function EventsEmit(eventName: string, ...data: any) {
  listeners.get(eventName)?.forEach((cb) => cb(...data));
}
export function EventsOn(eventName: string, cb: (...data: any) => void): () => void {
  let set = listeners.get(eventName);
  if (!set) { set = new Set(); listeners.set(eventName, set); }
  set.add(cb);
  return () => set!.delete(cb);
}
export function EventsOnMultiple(eventName: string, cb: (...data: any) => void, _n: number) {
  return EventsOn(eventName, cb);
}
export function EventsOnce(eventName: string, cb: (...data: any) => void) {
  const off = EventsOn(eventName, (...d) => { off(); cb(...d); });
  return off;
}
export function EventsOff(eventName: string, ..._rest: string[]) {
  listeners.delete(eventName);
}
export function EventsOffAll() { listeners.clear(); }

export function LogPrint(_m: string) {}
export function LogTrace(_m: string) {}
export function LogDebug(_m: string) {}
export function LogError(_m: string) {}
export function LogFatal(_m: string) {}
export function LogInfo(_m: string) {}
export function LogWarning(_m: string) {}

export function WindowReload() {}
export function WindowReloadApp() {}
export function WindowSetAlwaysOnTop(_b: boolean) {}
export function WindowSetSystemDefaultTheme() {}
export function WindowSetLightTheme() {}
export function WindowSetDarkTheme() {}
export function WindowCenter() {}
export function WindowSetTitle(_t: string) {}
export function WindowFullscreen() {}
export function WindowUnfullscreen() {}
export function WindowIsFullscreen() { return Promise.resolve(false); }
export function WindowGetSize() { return Promise.resolve({ w: 1440, h: 900 }); }
export function WindowSetSize(_w: number, _h: number) {}
export function WindowSetMaxSize(_w: number, _h: number) {}
export function WindowSetMinSize(_w: number, _h: number) {}
export function WindowSetPosition(_x: number, _y: number) {}
export function WindowGetPosition() { return Promise.resolve({ x: 0, y: 0 }); }
export function WindowHide() {}
export function WindowShow() {}
export function WindowMaximise() {}
export function WindowToggleMaximise() {}
export function WindowUnmaximise() {}
export function WindowIsMaximised() { return Promise.resolve(false); }
export function WindowMinimise() {}
export function WindowUnminimise() {}
export function WindowIsMinimised() { return Promise.resolve(false); }
export function WindowIsNormal() { return Promise.resolve(true); }
export function WindowSetBackgroundColour(_r: number, _g: number, _b: number, _a: number) {}
export function ScreenGetAll() { return Promise.resolve([]); }
export function BrowserOpenURL(_url: string) {}
export function Environment() {
  return Promise.resolve({ buildType: 'dev', platform: 'darwin', arch: 'arm64' });
}
export function Quit() {}
export function Hide() {}
export function Show() {}
export function ClipboardGetText() { return Promise.resolve(''); }
export function ClipboardSetText(_t: string) { return Promise.resolve(true); }

// File-drop hooks — accept the callback so subscribers don't blow up,
// but never fire.
export function OnFileDrop(
  _cb: (x: number, y: number, paths: string[]) => void,
  _useDropTarget?: boolean,
) {}
export function OnFileDropOff() {}

// CallByID / CallByName — not used by this frontend, stub for safety.
export function CallByID(_id: number, ..._args: any[]) { return Promise.resolve(); }
export function CallByName(_name: string, ..._args: any[]) { return Promise.resolve(); }
