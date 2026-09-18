// 中文字典汇总：DictKey 以此处的 key 联合为准
import common from './common'
import layout from './layout'
import login from './login'
import register from './register'
import keys from './keys'
import channels from './channels'
import models from './models'
import tokens from './tokens'
import guide from './guide'
import logs from './logs'
import stats from './stats'
import admin from './admin'
import backup from './backup'

const zh = Object.assign({}, common, layout, login, register, keys, channels, models, tokens, guide, logs, stats, admin, backup)

export type DictKey = keyof typeof zh

export default zh
