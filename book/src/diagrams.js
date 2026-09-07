// Keep the textual source available if rendering fails or JavaScript is disabled.
window.addEventListener('DOMContentLoaded', () => {
  const sources = [...document.querySelectorAll('code.language-mermaid')].map((source, index) => ({
    index,
    source: source.textContent,
    block: source.parentElement,
    figure: null,
  }));
  if (!sources.length) return;

  const root = document.documentElement;
  const darkThemes = new Set(['navy', 'coal', 'ayu']);
  const selectedTheme = () => [...darkThemes].some(name => root.classList.contains(name)) ? 'dark' : 'default';
  let renderedTheme;
  let renderCount = 0;
  let renderSequence = Promise.resolve();

  const render = async theme => {
    renderCount += 1;
    mermaid.initialize({startOnLoad: false, securityLevel: 'strict', theme});

    for (const diagram of sources) {
      try {
        const id = `pulse-diagram-${renderCount}-${diagram.index}`;
        const {svg} = await mermaid.render(id, diagram.source);
        if (diagram.figure) {
          diagram.figure.innerHTML = svg;
        } else {
          const figure = document.createElement('div');
          figure.className = 'pulse-diagram';
          figure.innerHTML = svg;
          diagram.block.replaceWith(figure);
          diagram.figure = figure;
        }
      } catch (error) {
        console.error('Pulse diagram failed to render', error);
        root.dataset.diagramError = 'true';
      }
    }

    renderedTheme = theme;
  };

  const renderSelectedTheme = () => {
    const theme = selectedTheme();
    if (theme === renderedTheme) return;
    renderSequence = renderSequence.then(() => render(theme));
  };

  new MutationObserver(renderSelectedTheme).observe(root, {
    attributes: true,
    attributeFilter: ['class'],
  });
  renderSelectedTheme();
});
