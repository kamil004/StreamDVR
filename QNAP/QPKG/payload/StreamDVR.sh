#!/bin/sh
CONF=/etc/config/qpkg.conf
QPKG_NAME="StreamDVR"
QPKG_ROOT=`/sbin/getcfg $QPKG_NAME Install_Path -f ${CONF}`
APACHE_ROOT=`/sbin/getcfg SHARE_DEF defWeb -d Qweb -f /etc/config/def_share.info`
export QNAP_QPKG=$QPKG_NAME

export QPKG_NAME QPKG_ROOT
export DESC=$QPKG_NAME
export CONFIG=$QPKG_ROOT/CONFIG_DIR
export SHELL=/bin/sh
export LC_ALL=en_US.UTF-8
export USER=admin
export LANG=en_US.UTF-8
export LC_CTYPE=en_US.UTF-8
export HOME=$QPKG_ROOT
export TMPDIR=$QPKG_ROOT/TMPDIR
mkdir -p $TMPDIR

export TZ=$(/sbin/getcfg System 'Time Zone' -f /etc/config/uLinux.conf)

export CB_PIDF=/var/run/chaturbate-dvr.pid
export TW_PIDF=/var/run/streamdvr-web.pid

case "$1" in
  start)
    ENABLED=$(/sbin/getcfg $QPKG_NAME Enable -u -d FALSE -f $CONF)
    if [ "$ENABLED" != "TRUE" ]; then
        echo "$QPKG_NAME is disabled."
        exit 1
    fi

/bin/ln -sf $QPKG_ROOT /opt/$QPKG_NAME

### Chaturbate recorder (port 8789)
if [ -n "$(ls $QPKG_ROOT/chaturbate-dvr* 2>/dev/null)" ]; then
/bin/ln -sf $QPKG_ROOT/chaturbate-dvr* /usr/bin/chaturbate-dvr
cd /share/Public
$QPKG_ROOT/chaturbate-dvr* --port=8789  &
echo $! > $CB_PIDF
$QPKG_ROOT/check_portCB.sh > /share/Public/check_portCB.log 2>&1 &
fi

### Stream DVR web / Twitch module (port 8090)
if [ -x $QPKG_ROOT/twitchdvr/streamdvr-web ]; then
chmod +x $QPKG_ROOT/twitchdvr/streamdvr-web
chmod +x $QPKG_ROOT/twitchdvr/ffmpeg 2>/dev/null
$QPKG_ROOT/twitchdvr/streamdvr-web -port 8090 -root $QPKG_ROOT > $TMPDIR/streamdvr-web.log 2>&1 &
echo $! > $TW_PIDF
fi

    ;;

  stop)

killall -9 check_portCB.sh 2>/dev/null

killall -9 streamdvr-web 2>/dev/null

ID=$(more /var/run/chaturbate-dvr.pid 2>/dev/null)

        if [ -e $CB_PIDF ]; then
            kill -9 $ID 2>/dev/null
            rm -f $CB_PIDF
        fi

if [ -e $TW_PIDF ]; then
    rm -f $TW_PIDF
fi

killall -9 chaturbate-dvr* 2>/dev/null

rm -rf $TMPDIR
rm -rf /usr/bin/chaturbate-dvr
rm -rf /opt/$QPKG_NAME

    ;;

  restart)
    $0 stop
    $0 start
    ;;

  *)
    echo "Usage: $0 {start|stop|restart}"
    exit 1
esac

exit 0