import type { ThreeEvent } from '@react-three/fiber';
import { twin } from '../store';

/** Shared pointer handlers: hover highlight, click to select, double-click to fly there. */
export function pickHandlers(id: string) {
  return {
    onClick: (e: ThreeEvent<MouseEvent>) => {
      e.stopPropagation();
      if (e.delta > 6) return;
      twin.select(id, e.detail >= 2);
    },
    onPointerOver: (e: ThreeEvent<PointerEvent>) => {
      e.stopPropagation();
      document.body.style.cursor = 'pointer';
      twin.set({ hovered: id });
    },
    onPointerOut: () => {
      document.body.style.cursor = '';
      if (twin.get().hovered === id) twin.set({ hovered: null });
    },
  };
}
