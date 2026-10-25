import Toybox.Graphics;
import Toybox.Lang;
import Toybox.WatchUi;

// A scrollable screen of paragraphs, for a workout's detail and a message.
// Each paragraph is [text, font, colour]; an empty text is a gap. UP and
// DOWN scroll by a screen; START runs the action, if there is one.
class TextView extends WatchUi.View {
    private var _paras as Array<Array>;
    // Wrapped lines as [text, font, colour], built on the first draw.
    private var _lines as Array<Array> or Null = null;
    private var _top as Number = 0;
    private var _shown as Number = 1;

    function initialize(paras as Array<Array>) {
        View.initialize();
        _paras = paras;
    }

    // Scrolls by a screen down (dir 1) or up (-1), keeping one line.
    function scroll(dir as Number) as Void {
        var lines = _lines;
        if (lines == null) {
            return;
        }
        var step = _shown > 1 ? _shown - 1 : 1;
        var top = _top + dir * step;
        if (top > lines.size() - _shown) {
            top = lines.size() - _shown;
        }
        _top = top < 0 ? 0 : top;
        WatchUi.requestUpdate();
    }

    function onUpdate(dc as Dc) as Void {
        var w = dc.getWidth();
        var h = dc.getHeight();
        // A round screen is narrowest at the top and bottom of the text
        // area: 74 % of the width fits from 18 % to 82 % of the height.
        var lines = _lines;
        if (lines == null) {
            lines = [] as Array<Array>;
            for (var i = 0; i < _paras.size(); i++) {
                var p = _paras[i];
                var font = p[1] as Graphics.FontType;
                var text = p[0] as String;
                if (text.length() == 0) {
                    lines.add(["", Graphics.FONT_XTINY, p[2]]);
                    continue;
                }
                var wrapped = Ui.wrap(dc, font, text, (w * 74) / 100);
                for (var j = 0; j < wrapped.size(); j++) {
                    lines.add([wrapped[j], font, p[2]]);
                }
            }
            _lines = lines;
        }

        dc.setColor(Graphics.COLOR_WHITE, Graphics.COLOR_BLACK);
        dc.clear();
        var top = (h * 18) / 100;
        var bottom = (h * 82) / 100;
        var y = top;
        var shown = 0;
        for (var i = _top; i < lines.size(); i++) {
            var font = lines[i][1] as Graphics.FontType;
            var text = lines[i][0] as String;
            var lh = text.length() == 0 ? dc.getFontHeight(font) / 2 : dc.getFontHeight(font);
            if (y + lh > bottom && shown > 0) {
                break;
            }
            dc.setColor(lines[i][2] as Graphics.ColorType, Graphics.COLOR_TRANSPARENT);
            dc.drawText(w / 2, y, font, text, Graphics.TEXT_JUSTIFY_CENTER);
            y += lh;
            shown += 1;
        }
        _shown = shown;

        // Arrows when there is more above or below.
        dc.setColor(Graphics.COLOR_LT_GRAY, Graphics.COLOR_TRANSPARENT);
        var a = h / 40 + 2;
        if (_top > 0) {
            var ay = top - 2 * a;
            dc.fillPolygon([[w / 2 - a, ay + a], [w / 2 + a, ay + a], [w / 2, ay]]);
        }
        if (_top + shown < lines.size()) {
            var ay = bottom + a;
            dc.fillPolygon([[w / 2 - a, ay], [w / 2 + a, ay], [w / 2, ay + a]]);
        }
    }
}

class TextDelegate extends WatchUi.BehaviorDelegate {
    private var _view as TextView;
    private var _action as Method() as Void or Null;

    function initialize(view as TextView, action as Method() as Void or Null) {
        BehaviorDelegate.initialize();
        _view = view;
        _action = action;
    }

    function onNextPage() as Boolean {
        _view.scroll(1);
        return true;
    }

    function onPreviousPage() as Boolean {
        _view.scroll(-1);
        return true;
    }

    function onSelect() as Boolean {
        var action = _action;
        if (action != null) {
            action.invoke();
            return true;
        }
        return false;
    }
}
