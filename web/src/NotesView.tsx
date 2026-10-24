import { Component, useEffect, useMemo, useState, type ReactNode } from "react";
import { entitiesLoaded, loadEntities, needsEntities, renderNotes } from "./markdown";

interface BoundaryProps {
  /** The notes as written: shown when rendering them fails. */
  text: string;
  children: ReactNode;
}

/**
 * Defence in depth around the rendered notes: should rendering a note
 * ever throw, the note shows as written instead of taking the page down.
 * Other text tries rendering again.
 */
export class NotesBoundary extends Component<BoundaryProps, { failed: boolean; text: string }> {
  state = { failed: false, text: this.props.text };

  static getDerivedStateFromProps(props: BoundaryProps, state: { failed: boolean; text: string }) {
    return props.text === state.text ? null : { failed: false, text: props.text };
  }

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) return <p className="notes-rendered__raw">{this.props.text}</p>;
    return this.props.children;
  }
}

/**
 * The rendered notes. A note with a `&name;` loads the entity table (a
 * chunk of its own, #134): until it is there the entity shows as
 * written, then the note renders again. Should loading fail, it stays as
 * written; other text tries again.
 */
function Rendered({ text }: { text: string }) {
  const [loaded, setLoaded] = useState(entitiesLoaded);
  const needed = useMemo(() => !loaded && needsEntities(text), [text, loaded]);
  useEffect(() => {
    if (!needed) return;
    let live = true;
    loadEntities().then(
      () => {
        if (live) setLoaded(true);
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, [needed, text]);
  return useMemo(() => renderNotes(text), [text, loaded]);
}

/** The notes rendered from their Markdown (mounted only while they are not edited). */
export function NotesView({ text }: { text: string }) {
  return (
    <NotesBoundary text={text}>
      <Rendered text={text} />
    </NotesBoundary>
  );
}
